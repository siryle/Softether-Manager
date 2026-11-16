package manager

import (
	"errors"
	"io"
	"os"
)

func cliPassword(password string, fromStdin bool) string {
	if !fromStdin {
		return password
	}
	if password != "" {
		panic(errors.New("use either --password or --password-stdin"))
	}
	data, err := io.ReadAll(io.LimitReader(os.Stdin, 1025))
	check(err)
	if len(data) > 1024 {
		panic(errors.New("password input is too long"))
	}
	return string(data)
}
