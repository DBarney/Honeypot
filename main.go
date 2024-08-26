package main

import (
	"botcheckup/html"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"hash/crc64"
	"io"
	"io/fs"
	"io/ioutil"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gliderlabs/ssh"
	_ "github.com/mattn/go-sqlite3"
)

type server struct {
	db *sql.DB
}

var servers = []string{
	"Apache/2.4.48 (Unix) OpenSSL/1.1.1k PHP/7.4.24",
	"nginx/1.21.0",
	"Microsoft-IIS/10.0",
	"LiteSpeed",
	"Node.js",
	"PHP/8.0.10",
	"Apache Tomcat/9.0.50",
	"Jetty(9.4.43.v20210629)",
	"rExpress",
	"Python/3.9.5",
}

func (s server) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	start := time.Now().Unix()
	address, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		address = req.RemoteAddr
	}
	// just the first 1K of the body. maybe increase this later
	req.Body = http.MaxBytesReader(res, req.Body, 1024)
	data, err := ioutil.ReadAll(req.Body)

	info := map[string]interface{}{
		"source":  "http",
		"address": address,
		"host":    req.Host,
		"header":  req.Header,
		"method":  req.Method,
		"url":     req.URL.String(),
		"proto":   req.Proto,
		"time":    start,
	}
	if err != nil {
		info["error"] = err.Error()
	}
	if data != nil {
		info["body"] = string(data)
	}

	bytes, err := json.Marshal(info)
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = s.db.Exec("insert into log (entry) values (?)", string(bytes))
	if err != nil {
		fmt.Println(err)
	}
	h := crc64.New(crc64.MakeTable(crc64.ECMA))
	h.Write([]byte(req.Host))
	h64, ok := h.(hash.Hash64)
	if !ok {
		panic("64 bit is not supported")
	}

	result := h64.Sum64()

	paths := []string{}
	err = fs.WalkDir(html.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			panic(err)
		}

		paths = append(paths, path)
		return nil
	})
	if err != nil {
		panic(err)
	}

	name := servers[result%uint64(len(servers))]
	path := paths[result%uint64(len(paths))]
	bytes, err = fs.ReadFile(html.Files, path)
	if err != nil {
		panic(err)
	}

	page := bytes

	res.Header()["Server"] = []string{name}
	res.Header().Set("Content-Length", strconv.Itoa(len(page)))
	res.Write(page)
}

type readWrapper struct {
	rows    *sql.Rows
	id      int
	current []byte
}

func (r *readWrapper) Read(p []byte) (int, error) {
	var next []byte
	n := 0
	if r.current == nil {
		if !r.rows.Next() {
			if r.id == 0 {
				p[0] = '['
				p[1] = ']'
				return 2, io.EOF
			}
			p[0] = ']'
			return 1, io.EOF
		}
		p[0] = ','
		if r.id == 0 {
			p[0] = '['
		}
		next = p[1:]
		n++

		err := r.rows.Scan(&r.id, &r.current)
		if err != nil {
			return 0, err
		}
	}
	bytes := copy(next, r.current)
	if bytes == len(r.current) {
		r.current = nil
	} else {
		r.current = r.current[bytes:]
	}

	return bytes + n, nil
}

func main() {
	debug := flag.Bool("debug", false, "listen on non protected ports, 2222, 8080")
	flag.Parse()
	sshPort := ":22"
	httpPort := ":80"
	url := "https://data.botcheckup.io:3001/collect"
	dbFile := "/srv/observer/sqlite/data.db"
	if *debug {
		sshPort = ":2222"
		httpPort = ":8080"
		dbFile = "data.db"
		url = "http://127.0.0.1:3002/collect"
	}
	db, err := sql.Open("sqlite3", dbFile)
	if err != nil {
		panic(err)
	}

	db.Exec("create table log (entry);")
	defer db.Close()
	s := &server{
		db: db,
	}
	srv := &http.Server{
		Addr:         httpPort,
		Handler:      s,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	server := &ssh.Server{
		Version:     "SSH-2.0-OpenSSH_8.4p1 Debian-5+deb11u1", // probably should rotate this around a bit
		Addr:        sshPort,
		IdleTimeout: time.Second * 5,
		MaxTimeout:  time.Second * 10,
		PasswordHandler: func(ctx ssh.Context, pass string) bool {
			start := time.Now().Unix()
			address, _, err := net.SplitHostPort(ctx.RemoteAddr().String())
			if err != nil {
				address = ctx.RemoteAddr().String()
			}
			info := map[string]interface{}{
				"type":       "ssh",
				"user":       ctx.User(),
				"pass":       pass,
				"client_ver": ctx.ClientVersion(),
				"address":    address,
				"time":       start,
			}
			bytes, err := json.Marshal(info)
			if err != nil {
				fmt.Println(err)
				return false
			}
			_, err = db.Exec("insert into log (entry) values (?)", string(bytes))
			if err != nil {
				fmt.Println(err)
			}
			// could I also present a fake shell to collect commands?
			return false
		},
	}
	go func() {
		err := server.ListenAndServe()
		if err != nil {
			panic(err)
		}
	}()

	report := func() {
		fmt.Println("reporting...")
		// now to stream the new data
		rows, err := db.Query("select rowid, entry from log")
		if err != nil {
			return
		}
		defer rows.Close()

		wrap := &readWrapper{rows: rows}
		// I need to establish a connection to our central server
		res, err := http.Post(url, "application/json", wrap)
		if err != nil {
			fmt.Println(err)
			return
		}
		fmt.Println(res, wrap.id)

		// and delete the old data
		if wrap.id > 0 {
			_, err = db.Exec("delete from log where rowid < ?", wrap.id)
			if err != nil {
				fmt.Println(err)
			}
		}
	}
	if *debug {
		report()
	}
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			report()
		}
	}()

	err = srv.ListenAndServe()
	if err != nil {
		panic(err)
	}

}
