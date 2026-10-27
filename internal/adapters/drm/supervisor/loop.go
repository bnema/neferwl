package supervisor

//hexcheck:ignore suspicious-business-logic-in-adapter multiplexes backend I/O and forwards apply events to core without making apply decisions
func (run *outputsRun) loop() error {
	for {
		if cfg, ok := run.apply.Next(); ok {
			run.configure(cfg)
			continue
		}
		configs, configNext := run.apply.ConfigOut(run.ch.Configured), run.apply.Config()
		replies, replyNext := run.apply.ReplyOut(run.ch.Replies)
		heads, headsNext := run.apply.HeadsOut(run.ch.Heads)
		select {
		case <-run.ctx.Done():
			return run.set.Wait()
		case failure := <-run.readerErr:
			if err, exit := run.onReaderFailure(failure); exit {
				return err
			}
		case state, ok := <-run.ch.SecurityChanges:
			if err, exit := run.onSecurity(state, ok); exit {
				return err
			}
		case <-run.revokeDeadline:
			run.revokeDeadline = nil
			run.barrier()
		case ev := <-run.hotplug:
			if err, exit := run.onHotplug(ev); exit {
				return err
			}
		case enabled := <-run.active:
			if err, exit := run.onSeat(enabled); exit {
				return err
			}
		case event := <-run.ch.LeaseRequests:
			if err, exit := run.onLease(event); exit {
				return err
			}
		case ev := <-run.ch.Reloads:
			run.apply.Reload(ev.Config)
		case req := <-run.ch.Requests:
			run.apply.Request(req)
		case configs <- configNext:
			run.apply.ConfigSent()
		case replies <- replyNext:
			if replyNext.Err != nil {
				run.appLog.Warn().Err(replyNext.Err).Msg("output configuration failed")
			}
			run.apply.ReplySent()
		case heads <- headsNext:
			run.apply.HeadsSent()
		case result := <-run.ready:
			run.complete(run.progress.ReadyEvent(result.name, result.source, run.readySources[result.name], result.err))
		case <-run.deadline:
			run.complete(run.progress.Timeout(run.op))
		case stopped := <-run.set.Stopped:
			if err, exit := run.onStopped(stopped); exit {
				return err
			}
		}
	}
}
