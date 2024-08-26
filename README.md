# Honeypot

Just a basic honey put written in go. I was curious how well chatgpt would generate some html pages. so I asked it to generate about 10 pages that looks similar to administrative pages to routeres, cameras, internals tools etc.

It did fairly good.

So I build a small collector that listens to http requests and sends back these fake pages.

After running it for a year, looks like almost 100% of the exploits I collected are for php web apps.

Just aks if you want to access the full data set.
