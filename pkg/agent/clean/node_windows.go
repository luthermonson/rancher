package clean

import (
	"fmt"
	"time"
)

func Job() error {
	// TODO
	return nil
}

func Node() error {
	//TODO
	for {
		fmt.Println("waiting...")
		time.Sleep(15 * time.Second)
	}
	return nil
}

func Links() error {
	// TODO
	return nil
}

func Paths() error {
	// TODO
	return nil
}

func IpTables() error {
	// TODO
	return nil
}
