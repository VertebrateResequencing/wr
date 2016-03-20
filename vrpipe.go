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
        stats := job.Stats();
        fmt.Printf("stats: %s; time left: %d\n", stats.State, stats.TimeLeft)
        stats2 := jobqueue.Stats();
        fmt.Printf("ready: %d; reserved: %d\n", stats2.Ready, stats2.Reserved)
        job.Delete();
    }
    
    stats := jobqueue.DaemonStats();
    fmt.Printf("producers: %d, workers: %d, pid: %d, hostname: %s\n", stats.Producers, stats.Workers, stats.Pid, stats.Hostname)
    
    jobqueue.Disconnect()
    fmt.Printf("All done.\n")
}

