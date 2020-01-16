package processor

import (
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/bketelsen/logr"
	"github.com/joadha/rabbitmq-cli-consumer/acknowledger"
	"github.com/joadha/rabbitmq-cli-consumer/collector"
	"github.com/joadha/rabbitmq-cli-consumer/command"
	"github.com/joadha/rabbitmq-cli-consumer/delivery"
	"github.com/prometheus/client_golang/prometheus"
)

// Processor describes the interface used by the consumer to process messages.
type Processor interface {
	Process(delivery.Delivery) error
}

// New creates a new processor instance.
func New(b command.Builder, a acknowledger.Acknowledger, l logr.Logger) Processor {
	return &processor{builder: b, ack: a, log: l}
}

type processor struct {
	Processor
	builder command.Builder
	ack     acknowledger.Acknowledger
	log     logr.Logger
	mu      sync.Mutex
	cmd     *exec.Cmd
}

// Process creates a new exec command using the builder and executes the command. The message gets acknowledged
// according to the commands exit code using the acknowledger.
func (p *processor) Process(d delivery.Delivery) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	var err error

	p.cmd, err = p.builder.GetCommand(d.Properties(), d.Info(), d.Body())
	defer func() {
		p.cmd = nil
	}()
	if err != nil {
		d.Nack(true)
		return NewCreateCommandError(err)
	}

	start := time.Now()
	exitCode := p.run()

	collector.ProcessCounter.With(prometheus.Labels{"exit_code": strconv.Itoa(exitCode)}).Inc()
	collector.ProcessDuration.Observe(time.Since(start).Seconds())
	if !d.Properties().Timestamp.IsZero() {
		collector.MessageDuration.Observe(time.Since(d.Properties().Timestamp).Seconds())
	}

	if err := p.ack.Ack(d, exitCode); err != nil {
		return NewAcknowledgmentError(err)
	}

	return nil
}

func (p *processor) run() int {
	p.log.Info("Processing message...")
	defer p.log.Info("Processed!")

	var out []byte
	var err error
	capture := p.cmd.Stdout == nil && p.cmd.Stderr == nil

	if capture {
		out, err = p.cmd.CombinedOutput()
	} else {
		err = p.cmd.Run()
	}

	if err != nil {
		p.log.Info("Failed. Check error log for details.")
		p.log.Errorf("Error: %s\n", err)
		if capture {
			p.log.Errorf("Failed: %s", string(out))
		}

		return exitCode(err)
	}

	return 0
}

func exitCode(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}

	return 1
}
