.PHONY: all build clean install test coverage

all: clean build install

clean:
	go clean -r -x
	-rm -rf _output

build:
	go build ./iscsi/
	go build -o _output/example ./example/main.go

install:
	go install ./iscsi/

test:
	go test ./iscsi/

coverage:
	go test ./iscsi -coverprofile=coverage.out
	go tool cover -html=coverage.out
