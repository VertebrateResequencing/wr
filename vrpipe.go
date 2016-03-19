package main

import (
    "fmt"
    "time"
    "github.com/sb10/vrpipe/jobqueue"
)

const (
    tubeDES = "des"
)

func main() {
    fmt.Printf("Will try to connect to beanstalk...\n")
    
    jobqueue := jobqueue.Connect("localhost:11300", tubeDES)
    
    jobqueue.Add("test job 1", 30)
    jobqueue.Add("test job 2", 40)
    
    for {
        job := jobqueue.Reserve(5*time.Second)
        if job == nil {
            break
        }
        job.Stats();
        job.Delete();
    }
    
    jobqueue.Disconnect()
    fmt.Printf("All done.\n")
}

