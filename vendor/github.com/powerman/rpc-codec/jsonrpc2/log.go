package jsonrpc2

import "log"

func logIfFail(f func() error) {
	if err := f(); err != nil {
		log.Print(err)
	}
}
