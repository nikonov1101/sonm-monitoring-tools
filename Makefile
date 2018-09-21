.PHONY: relay-mon rv-mon map-proxy

all: relay-mon rv-mon

clean:
	rm -f relay_mon rv_mon map_proxy

relay-mon:
	go build -tags 'nocgo' -o relay_mon relay-mon/main.go

rv-mon:
	go build -tags 'nocgo' -o rv_mon rv-mon/main.go

map-proxy:
	go build -tags 'nocgo' -o map_proxy map-proxy/main.go