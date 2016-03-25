// Copyright © 2016 Genome Research Limited
// Author: Sendu Bala <sb10@sanger.ac.uk>.
//
//  This file is part of VRPipe.
//
//  VRPipe is free software: you can redistribute it and/or modify
//  it under the terms of the GNU Lesser General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  VRPipe is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU Lesser General Public License for more details.
//
//  You should have received a copy of the GNU Lesser General Public License
//  along with VRPipe. If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"fmt"
    "time"
	"github.com/spf13/cobra"
    "github.com/sb10/vrpipe/queue"
)

type MyStruct struct {
    Num int
    Foo string
}

// setupCmd represents the setup command
var queueCmd = &cobra.Command{
	Use:   "queue",
	Short: "temp playground for queue implementations",
	Long: `don't use this`,
	Run: func(cmd *cobra.Command, args []string) {
		myqueue := queue.New("test queue")
        stats := myqueue.Stats()
        
        foo := &MyStruct{Num: 1, Foo: "bar"}
        myqueue.Add("myfoo", foo, 0, 100 * time.Millisecond, 100 * time.Millisecond)
        
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        
        fmt.Println("\nwill reserve...")
        item, exists := myqueue.Reserve()
        if exists {
            foo := item.Data.(*MyStruct)
            fmt.Printf("got item with key %s and num %d\n", item.Key, foo.Num)
        } else {
            fmt.Println("nothing in ready queue")
        }
        
        <-time.After(150 * time.Millisecond)
        
        fmt.Println("\nafter 150 seconds will reserve again...")
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        item, exists = myqueue.Reserve()
        if exists {
            foo := item.Data.(*MyStruct)
            fmt.Printf("got item with key %s and num %d\n", item.Key, foo.Num)
        } else {
            fmt.Println("nothing in ready queue")
        }
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        
        <-time.After(50 * time.Millisecond)
        
        fmt.Println("\nafter 50 more ms will reserve again...")
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        item, exists = myqueue.Reserve()
        if exists {
            foo := item.Data.(*MyStruct)
            fmt.Printf("got item with key %s and num %d\n", item.Key, foo.Num)
        } else {
            fmt.Println("nothing in ready queue")
        }
        
        <-time.After(150 * time.Millisecond)
        
        fmt.Println("\nafter 150 more ms will reserve again...")
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        item, exists = myqueue.Reserve()
        if exists {
            foo := item.Data.(*MyStruct)
            fmt.Printf("got item with key %s and num %d\n", item.Key, foo.Num)
        } else {
            fmt.Println("nothing in ready queue")
        }
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        
        released := myqueue.Release("myfoo")
        fmt.Printf("\nrelease myfoo returned %v, and item state is %s\n", released, item.State)
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        
        buried := myqueue.Bury("myfoo")
        fmt.Printf("\nbury myfoo returned %v, and item state is %s\n", buried, item.State)
        item, exists = myqueue.Reserve()
        if exists {
            foo := item.Data.(*MyStruct)
            fmt.Printf("reserved again and got item with key %s and num %d\n", item.Key, foo.Num)
            
            buried = myqueue.Bury("myfoo")
            fmt.Printf("bury myfoo returned %v, and item state is %s\n", buried, item.State)
            stats = myqueue.Stats()
            fmt.Printf("queue stats: %v\n", stats)
            
            kicked := myqueue.Kick("myfoo")
            fmt.Printf("\nkick myfoo returned %v, and item state is %s\n", kicked, item.State)
            stats = myqueue.Stats()
            fmt.Printf("queue stats: %v\n", stats)
            
            <-time.After(150 * time.Millisecond)
            
            fmt.Printf("\nafter waiting 150 ms...\n")
            stats = myqueue.Stats()
            fmt.Printf("queue stats: %v\n", stats)
        }
        
        removed := myqueue.Remove("myfoo")
        fmt.Printf("\nremove myfoo returned %v, and item state is %s\n", removed, item.State)
        stats = myqueue.Stats()
        fmt.Printf("queue stats: %v\n", stats)
        
        myqueue.Add("j1", "j1", 0, 0 * time.Second, 1 * time.Second)
        myqueue.Add("j2", "j2", 0, 0 * time.Second, 1 * time.Second)
        myqueue.Add("j3", "j3", 0, 0 * time.Second, 1 * time.Second)
        <-time.After(10 * time.Millisecond)
        j1, exists1 := myqueue.Reserve()
        j2, exists2 := myqueue.Reserve()
        j3, exists3 := myqueue.Reserve()
        if exists1 && exists2 && exists3 {
            myqueue.Bury("j1")
            myqueue.Bury("j2")
            myqueue.Bury("j3")
            fmt.Printf("\nafter adding, reserving and burying 3 jobs...\n")
            stats = myqueue.Stats()
            fmt.Printf("queue stats: %v\n", stats)
            myqueue.Kick("j2")
            <-time.After(10 * time.Millisecond)
            fmt.Printf("after kicking the second job, the status of 1 is %s, 2 is %s and 3 is %s\n", j1.State, j2.State, j3.State)
            stats = myqueue.Stats()
            fmt.Printf("queue stats: %v\n", stats)
        }
	},
}

func init() {
	RootCmd.AddCommand(queueCmd)
}
