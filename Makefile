.PHONY: relay-mon rv-mon

all: relay-mon rv-mon

relay-mon:
	go build -tags 'nocgo' -o relay_mon relay-mon/main.go

rv-mon:
	go build -tags 'nocgo' -o rv_mon rv-mon/main.go
