package main

import (
    "fmt"
    "time"
    "log"
    "github.com/sb10/gobeanstalk"
)

const (
    tubeDES = "des"
)

func beanstalkConnect(url string) *gobeanstalk.Conn {
    conn, err := gobeanstalk.Dial(url)
    if err != nil {
        log.Fatal("Failed to connect to beanstalkd: ", err.Error())
    }
    return conn
}

func beanstalkAdd(beanstalk *gobeanstalk.Conn, tubename string, jobBody string, ttr int) {
    err := beanstalk.Use(tubename)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to use %s: ", tubename), err.Error())
    }
    job, err := beanstalk.Put([]byte(jobBody), 0, 0*time.Second, time.Duration(ttr)*time.Second)
    if err != nil {
        log.Fatal("Failed to add a new job to beanstalk: ", err.Error())
    }
    
    fmt.Printf("Added job %d\n", job)
}

func beanstalkReserve(beanstalk *gobeanstalk.Conn, tubename string, timeout time.Duration) *gobeanstalk.Job {
    _, err := beanstalk.Watch(tubename)
    if err != nil {
        log.Fatal(err)
    }
    job, err := beanstalk.Reserve(timeout)
    if err == gobeanstalk.ErrTimedOut {
        return nil
    }
    if err != nil {
        log.Fatal(err)
    }
    return job
}

func beanstalkJobStats(beanstalk *gobeanstalk.Conn, job *gobeanstalk.Job) {
    yaml, err := beanstalk.StatsJob(job.ID)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to get stats for beanstalk job %d: ", job.ID), err.Error())
    }
    fmt.Printf(string(yaml))
}

func beanstalkJobDelete(beanstalk *gobeanstalk.Conn, job *gobeanstalk.Job) {
    err := beanstalk.Delete(job.ID)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to delete beanstalk job %d: ", job.ID), err.Error())
    }
}

func main() {
    fmt.Printf("Will try to connect to beanstalk...\n")
    
    beanstalk := beanstalkConnect("localhost:11300")
    
    beanstalkAdd(beanstalk, tubeDES, "test job 1", 30)
    beanstalkAdd(beanstalk, tubeDES, "test job 2", 40)
    
    var job *gobeanstalk.Job;
    for {
        job = beanstalkReserve(beanstalk, tubeDES, 5*time.Second)
        if job == nil {
            break
        }
        beanstalkJobStats(beanstalk, job);
        beanstalkJobDelete(beanstalk, job);
    }
    
    fmt.Printf("All done.\n")
}

