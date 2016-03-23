// package jobqueue combines beanstalkd with redis to form a reliable job
// queue system (beanstalkd) that can't have duplicate jobs and lets you look
// up jobs by their bodies (redis), instead of their beanstalk job id
package jobqueue

import (
    "fmt"
    "time"
    "strings"
    "net"
    "github.com/sb10/gobeanstalk"
    "github.com/sb10/vrpipe/ssh"
    "gopkg.in/yaml.v2"
)

const (
    TubeDES = "des"
)

// Conn represents a connection to both the job queue daemon (beanstalkd)
// and to the lookup daemon (redis), specific to a particular queue (aka tube)
type Conn struct {
    beanstalk *gobeanstalk.Conn
    tube string
    used bool
    watched bool
}

// TubeStats represents the stats of a beanstalk tube
type TubeStats struct {
    Ready uint64 `yaml:"current-jobs-ready"`
    Reserved uint64 `yaml:"current-jobs-reserved"`
    Delayed uint64 `yaml:"current-jobs-delayed"`
    Buried uint64 `yaml:"current-jobs-buried"`
    Waiting uint64 `yaml:"current-waiting"`
    Watching uint64 `yaml:"current-watching"`
}

// BeanstalkStats represents the stats of the beanstalkd daemon
type BeanstalkStats struct {
    Connections uint64 `yaml:"current-connections"`
    Producers uint64 `yaml:"current-producers"`
    Workers uint64 `yaml:"current-workers"`
    Waiting uint64 `yaml:"current-waiting"`
    Pid uint64
    Hostname string
}

// Job represents a beanstalkd job
type Job struct {
    ID   uint64
    Body []byte
    conn *Conn
}

// JobStats represents the stats on a beanstalkd job
type JobStats struct {
    Tube string
    State string
    Priority uint32 `yaml:"pri"`
    Age uint64
    TimeLeft uint64 `yaml:"time-left"`
    Reserves uint32
    Timeouts uint32
    Releases uint32
    Buries uint32
    Kicks uint32
}

// Connect creates a connection to the job queue daemon (beanstalkd) and to
// the lookup daemon (redis), specific to a single queue (aka tube). If the
// spawn argument is set to true, then we will attempt to start up beanstalkd
// and redis if they're not already running.
func Connect(url string, tubename string, spawn bool) (conn *Conn, err error) {
    bconn, err := gobeanstalk.Dial(url)
    if err != nil {
        err = fmt.Errorf("Failed to connect to beanstalkd: %s\n", err.Error())
        
        // on connection refused we'll assume it simply isn't running and try
        // to start it up
        if spawn && strings.Contains(err.Error(), "connection refused") {
            host, port, err2 := net.SplitHostPort(url)
            if err2 != nil { 
                err = fmt.Errorf("%sAlso failed to parse [%s] as a place to ssh to: %s\n", err.Error(), url, err2.Error())
                return 
            }
            
            _, err2 = ssh.RunCmd(host, 22, "beanstalkd -p " + port, true)
            if err2 != nil { 
                err = fmt.Errorf("%sAlso failed to start up beanstalkd on %s: %s\n", err.Error(), host, err2.Error())
                return
            }
            
            time.Sleep(2*time.Second)
            bconn, err = gobeanstalk.Dial(url)
            if err != nil {
                return
            }
            err = nil
        } else {
            return
        }
    }
    
    conn = &Conn{bconn, tubename, false, false}
    return
}

// use ensures that use has been called for the desired tube
func (c *Conn) use() (err error) {
    if ! c.used {
        err = c.beanstalk.Use(c.tube)
        if err != nil {
            err = fmt.Errorf("Failed to use tube %s: %s\n", c.tube, err.Error())
            return
        }
        c.used = true;
    }
    return
}

// watch ensures that watch has been called for the desired tube
func (c *Conn) watch() (err error) {
    if ! c.watched {
        _, err = c.beanstalk.Watch(c.tube)
        if err != nil {
            err = fmt.Errorf("Failed to watch tube %s: %s\n", c.tube, err.Error())
            return
        }
        c.watched = true;
    }
    return
}

// Disconnect closes the connections to beanstalkd and redis
func (c *Conn) Disconnect() {
    c.beanstalk.Quit()
}

// Stats returns stats of the beanstalkd tube you connected to.
func (c *Conn) Stats() (s TubeStats, err error) {
    data, err := c.beanstalk.StatsTube(c.tube)
    if err != nil {
        err = fmt.Errorf("Failed to get stats for beanstalk tube %s: %s\n", c.tube, err.Error())
        return
    }
    
    s = TubeStats{}
    err = yaml.Unmarshal(data, &s)
    if err != nil {
        err = fmt.Errorf("Failed to parse yaml for beanstalk tube %s stats: %s", c.tube, err.Error())
    }
    return
}

// DaemonStats returns stats of the beanstalkd daemon itself.
func (c *Conn) DaemonStats() (s BeanstalkStats, err error) {
    data, err := c.beanstalk.Stats()
    if err != nil {
        err = fmt.Errorf("Failed to get stats for beanstalkd: %s\n", err.Error())
        return
    }
    
    s = BeanstalkStats{}
    err = yaml.Unmarshal(data, &s)
    if err != nil {
        err = fmt.Errorf("Failed to parse yaml for beanstalkd stats: %s\n", err.Error())
    }
    return
}

// Add adds a new job the job queue, but only if the job isn't already
// in there according to our parallel queue in redis. The ttr is the "time to
// release", meaning that if this job is reserved by a process, but that
// process exits before releasing, burying or deleting the job, the job will
// be automatically released ttr seconds after it was reserved (or last
// touched).
func (c *Conn) Add(jobBody string, ttr int) (j *Job, err error) {
    err = c.use()
    if err != nil {
        return
    }
    
    job, err := c.beanstalk.Put([]byte(jobBody), 0, 0*time.Second, time.Duration(ttr)*time.Second)
    if err != nil {
        err = fmt.Errorf("Failed to add a new job to beanstalk: %s\n", err.Error())
        return
    }
    
    j = &Job{job, []byte(jobBody), c}
    return
}

// Reserve takes a job off the job queue and notes in our parallel redis queue
// that it has been taken as well. If you process the job successfully you
// should Delete() it. If you can't deal with it right now you should Release()
// it. If you think it can never be dealt with you should Bury() it. If you die
// unexpectedly, the job will automatically be released back to the queue after
// the job's ttr runs down. If no job was available in the queue for as long as
// the timeout arguement, nil is returned for both job and error.
func (c *Conn) Reserve(timeout time.Duration) (j *Job, err error) {
    err = c.watch()
    if err != nil {
        return
    }
    
    job, err := c.beanstalk.Reserve(timeout)
    if err == gobeanstalk.ErrTimedOut {
        err = nil
        return
    }
    if err != nil {
        err = fmt.Errorf("Failed to reserve a job from beanstalk: %s\n", err.Error())
        return
    }
    
    j = &Job{job.ID, job.Body, c}
    return
}

// Stats returns stats of a beanstalkd job.
func (j *Job) Stats() (s JobStats, err error) {
    data, err := j.conn.beanstalk.StatsJob(j.ID)
    if err != nil {
        err = fmt.Errorf("Failed to get stats for beanstalk job %d: %s\n", j.ID, err.Error())
        return
    }
    
    s = JobStats{}
    err = yaml.Unmarshal(data, &s)
    if err != nil {
        err = fmt.Errorf("Failed to parse yaml for beanstalk job %d stats: %s\n", j.ID, err.Error())
    }
    return
}

// Delete removes a job from the beanstalkd queue and the parallel redis queue,
// for use after you have run the job successfully. Note that you must reserve a
// job before you can delete it.
func (j *Job) Delete() (err error) {
    err = j.conn.beanstalk.Delete(j.ID)
    if err != nil {
        err = fmt.Errorf("Failed to delete beanstalk job %d: %s\n", j.ID, err.Error())
    }
    return
}

// Release places a job back on the beanstalkd queue and updates the parallel
// redis queue, for use when you can't handle the job right now (eg. there was a
// suspected transient error) but maybe someone else can later. Note that you
// must reserve a job before you can release it.
func (j *Job) Release() (err error) {
    err = j.conn.beanstalk.Release(j.ID, 0, 60*time.Second)
    if err != nil {
        err = fmt.Errorf("Failed to release beanstalk job %d: %s\n", j.ID, err.Error())
    }
    return
}

// Touch resets a job's ttr in beanstalkd and redis, allowing you more time to
// work on it. Note that you must reserve a job before you can touch it.
func (j *Job) Touch() (err error) {
    err = j.conn.beanstalk.Touch(j.ID)
    if err != nil {
        err = fmt.Errorf("Failed to touch beanstalk job %d: %s\n", j.ID, err.Error())
    }
    return
}

// Bury marks a job in beanstalkd and redis as unrunnable, so it will be ignored
// (until the user does something to perhaps make it runnable and kicks the
// job). Note that you must reserve a job before you can bury it.
// 
func (j *Job) Bury() (err error) {
    err = j.conn.beanstalk.Bury(j.ID, 0)
    if err != nil {
        err = fmt.Errorf("Failed to bury beanstalk job %d: %s\n", j.ID, err.Error())
    }
    return
}

// Kick makes a previously Bury()'d job runnable again (it can be reserved in
// the future).
func (j *Job) Kick() (err error) {
    err = j.conn.beanstalk.KickJob(j.ID)
    if err != nil {
        err = fmt.Errorf("Failed to kick beanstalk job %d: %s\n", j.ID, err.Error())
    }
    return
}