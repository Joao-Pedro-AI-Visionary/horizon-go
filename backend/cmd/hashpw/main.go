// Command hashpw prints the bcrypt hash (cost 12) of the password read from
// stdin, for ADMIN_PASSWORD_HASH:
//
//	printf '%s' 'nova-senha' | go run ./cmd/hashpw
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	pw, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && pw == "" {
		fmt.Fprintln(os.Stderr, "hashpw: send the password on stdin")
		os.Exit(1)
	}
	pw = strings.TrimRight(pw, "\r\n")
	if len(pw) < 12 {
		fmt.Fprintln(os.Stderr, "hashpw: use at least 12 characters")
		os.Exit(1)
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hashpw:", err)
		os.Exit(1)
	}
	fmt.Println(string(h))
}
