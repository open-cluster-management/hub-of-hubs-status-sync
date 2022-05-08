// Copyright (c) 2021 Red Hat, Inc.
// Copyright Contributors to the Open Cluster Management project

package dbsyncers

import (
	"context"
	"time"
)

type genericDBSyncer struct {
	syncInterval time.Duration
	syncFunc     func(ctx context.Context)
}

func (syncer *genericDBSyncer) Start(ctx context.Context) error {
	ctxWithCancel, cancelContext := context.WithCancel(ctx)
	defer cancelContext()

	go syncer.periodicSync(ctxWithCancel)

	<-ctx.Done() // blocking wait for stop event

	return nil // context cancel is called before exiting this function
}

func (syncer *genericDBSyncer) periodicSync(ctx context.Context) {
	ticker := time.NewTicker(syncer.syncInterval)

	var (
		cancelFunc     context.CancelFunc
		ctxWithTimeout context.Context
	)

	for {
		select {
		case <-ctx.Done(): // we have received a signal to stop
			ticker.Stop()

			if cancelFunc != nil {
				cancelFunc()
			}

			return

		case <-ticker.C:
			// cancel the operation of the previous tick
			if cancelFunc != nil {
				cancelFunc()
			}

			ctxWithTimeout, cancelFunc = context.WithTimeout(ctx, syncer.syncInterval)

			syncer.syncFunc(ctxWithTimeout)
		}
	}
}
