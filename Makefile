relay-mon:
	go build -tags 'nocgo' -o relay_mon relay_mon/main.go

rv-mon:
	go build -tags 'nocgo' -o rv_mon rv-mon/main.go
