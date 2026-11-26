package repro_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/tmc/fuzztape/repro"
)

// Repro formats a failing input and its decoded op trace as a standalone
// regression test.
func ExampleRepro() {
	src := repro.Repro("TestQueueRepro", "queueMachine", []byte{0x00, 0x01}, []string{"push", "pop"})
	fmt.Print(src)
	// Output:
	// func TestQueueRepro(t *testing.T) {
	// 	// 2 bytes, decoding to 2 ops:
	// 	//	push
	// 	//	pop
	// 	data := []byte("\x00\x01")
	// 	queueMachine.Replay(t, data)
	// }
}

// WriteFile writes a complete, gofmt'd regression test file to disk.
func ExampleWriteFile() {
	dir, err := os.MkdirTemp("", "fuzztape-repro-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "repro_test.go")
	if err := repro.WriteFile(path, "queue_test", "TestQueueRepro", "queueMachine", []byte{0x00}, []string{"push"}); err != nil {
		log.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(len(data) > 0)
	// Output:
	// true
}
