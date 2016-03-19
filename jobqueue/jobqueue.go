// package jobqueue combines beanstalkd with redis to form a reliable job
// queue system (beanstalkd) that can't have duplicate jobs and lets you look
// up jobs by their bodies (redis), instead of their beanstalk job id
package jobqueue

import (
    "fmt"
    "time"
    "log"
    "github.com/sb10/gobeanstalk"
)

// Conn represents a connection to both the job queue daemon (beanstalkd)
// and to the lookup daemon (redis), specific to a particular queue (aka tube)
type Conn struct {
    beanstalk *gobeanstalk.Conn
    tube string
}

// Job represents a beanstalkd job
type Job struct {
    ID   uint64
    Body []byte
    conn *Conn
}

// Connect creates a connection to the job queue daemon (beanstalkd) and to
// the lookup daemon (redis), specific to a single queue (aka tube)
func Connect(url string, tubename string) *Conn {
    conn, err := gobeanstalk.Dial(url)
    if err != nil {
        log.Fatal("Failed to connect to beanstalkd: ", err.Error())
    }
    err = conn.Use(tubename)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to use %s: ", tubename), err.Error())
    }
    _, err = conn.Watch(tubename)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to watch %s: ", tubename), err.Error())
    }
    return &Conn{conn, tubename}
}

// Disconnect closes the connections to beanstalkd and redis
func (c *Conn) Disconnect() {
    c.beanstalk.Quit()
}

// Add adds a new job the job queue, but only if the job isn't already
// in there according to our parallel queue in redis. The ttr is the "time to
// release", meaning that if this job is reserved by a process, but that
// process exits before releasing, burying or deleting the job, the job will
// be automatically released ttr seconds after it was reserved (or last
// touched).
func (c *Conn) Add(jobBody string, ttr int) *Job {
    job, err := c.beanstalk.Put([]byte(jobBody), 0, 0*time.Second, time.Duration(ttr)*time.Second)
    if err != nil {
        log.Fatal("Failed to add a new job to beanstalk: ", err.Error())
    }
    
    fmt.Printf("Added job %d\n", job)
    
    return &Job{job, []byte(jobBody), c}
}

// Reserve takes a job off the job queue and notes in our parallel redis queue
// that it has been taken as well. If you process the job successfully you
// should Delete() it. If you can't deal with it right now you should Release()
// it. If you think it can never be dealt with you should Bury() it. If you die
// unexpectedly, the job will automatically be released back to the queue after
// the job's ttr runs down.
func (c *Conn) Reserve(timeout time.Duration) *Job {
    job, err := c.beanstalk.Reserve(timeout)
    if err == gobeanstalk.ErrTimedOut {
        return nil
    }
    if err != nil {
        log.Fatal(err)
    }
    return &Job{job.ID, job.Body, c}
}

// Stats returns the raw YAML from beanstalkd giving the stats of a job.
func (j *Job) Stats() []byte {
    yaml, err := j.conn.beanstalk.StatsJob(j.ID)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to get stats for beanstalk job %d: ", j.ID), err.Error())
    }
    fmt.Printf(string(yaml))
    return yaml
}

// Delete removes a job from the beanstalkd queue and the parallel redis queue,
// for use after you have run the job successfully. Note that you must reserve a
// job before you can delete it.
func (j *Job) Delete() {
    err := j.conn.beanstalk.Delete(j.ID)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to delete beanstalk job %d: ", j.ID), err.Error())
    }
}

// Release places a job back on the beanstalkd queue and updates the parallel
// redis queue, for use when you can't handle the job right now (eg. there was a
// suspected transient error) but maybe someone else can later. Note that you
// must reserve a job before you can release it.
func (j *Job) Release() {
    err := j.conn.beanstalk.Release(j.ID, 0, 60*time.Second)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to release beanstalk job %d: ", j.ID), err.Error())
    }
}

// Touch resets a job's ttr in beanstalkd and redis, allowing you more time to
// work on it. Note that you must reserve a job before you can touch it.
func (j *Job) Touch() {
    err := j.conn.beanstalk.Touch(j.ID)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to touch beanstalk job %d: ", j.ID), err.Error())
    }
}

// Bury marks a job in beanstalkd and redis as unrunnable, so it will be ignored
// (until the user does something to perhaps make it runnable and kicks the
// job). Note that you must reserve a job before you can bury it.
// 
func (j *Job) Bury() {
    err := j.conn.beanstalk.Bury(j.ID, 0)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to bury beanstalk job %d: ", j.ID), err.Error())
    }
}

// Kick makes a previously Bury()'d job runnable again (it can be reserved in
// the future).
func (j *Job) Kick() {
    err := j.conn.beanstalk.KickJob(j.ID)
    if err != nil {
        log.Fatal(fmt.Sprintf("Failed to kick beanstalk job %d: ", j.ID), err.Error())
    }
}