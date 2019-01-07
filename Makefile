.PHONY: all linux

all: linux

clean:
	rm -f mon-put-instance-data-Linux-*

linux:
	env CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o mon-put-instance-data-Linux-x86_64
