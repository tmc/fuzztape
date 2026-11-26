package corpus_test

import (
	"fmt"
	"log"
	"os"

	"github.com/tmc/fuzztape/corpus"
)

// Save writes a corpus entry in go test's fuzz v1 format and Seeds loads
// every entry in a directory back in deterministic filename order.
func ExampleSave() {
	dir, err := os.MkdirTemp("", "fuzztape-corpus-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	if _, err := corpus.Save(dir, []byte{0x01, 0x02, 0x03}); err != nil {
		log.Fatal(err)
	}
	seeds, err := corpus.Seeds(dir)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d seed: %x\n", len(seeds), seeds[0])
	// Output:
	// 1 seed: 010203
}
