trackl: *.go htmx.min.js
	go build .

htmx.min.js:
	wget -O $@ https://unpkg.com/htmx.org@1.9.9/dist/htmx.min.js

docker: Dockerfile
	podman build .
