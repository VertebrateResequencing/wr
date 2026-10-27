// Command bigcorpus copies the commands of at least 1KB from one zc corpus to
// another: the large real commands that portal-sized test jobs are built from.
//
// usage: bigcorpus <in> <out>
package main

import (
	"fmt"
	"os"

	"github.com/VertebrateResequencing/wr/customdbproto/internal/zc"
)

func main() {
	c, err := zc.ReadCorpus(os.Args[1])
	if err != nil {
		panic(err)
	}

	var big []string

	for _, s := range c {
		if len(s) >= 1024 {
			big = append(big, s)
		}
	}

	fmt.Println("commands", len(c), "of at least 1KB", len(big))

	if err := zc.WriteCorpus(os.Args[2], big); err != nil {
		panic(err)
	}
}
