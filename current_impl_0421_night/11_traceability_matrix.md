# Traceability Matrix (Docs -> Source Files -> Symbols)

## 00_index.md

- files: `src/main.go`, `src/boot.S`, `src/userspace.go`, `src/netsock.go`
- symbols: `main`, `_start`, `syscallDispatch`, `sysSocketHandler`

## 01_boot_and_kernel_init.md

- files: `src/boot.S`, `src/main.go`, `src/smp.go`, `src/lapic_timer.go`
- symbols:
  - boot: `_start`, `long_mode_start`
  - init: `main`, `afterTicksInit`, `smpInit`, `gdtInit`, `lapicTimerCalibrate`, `lapicTimerInit`
  - gates: `gdtReady`, `bspBootDone`

## 02_cpu_descriptors_traps_interrupts.md

- files: `src/idt.go`, `src/isr.S`, `src/interrupt.go`, `src/gdt.go`, `src/userspace.go`
- symbols:
  - IDT: `IDTEntry`, `idtInit`, `setGate`, `setGateDPL3`, `idtLoadAP`
  - ISR: `isr_common`, `isr_table`
  - dispatch: `go_interrupt_handler`, `registerHandler`
  - syscall frame: `SyscallFrame`
  - GDT/TSS: `gdtInit`, `gdtInitPerCPU`, `tssSetRSP0`

## 03_smp_lapic_timer_ipi.md

- files: `src/smp.go`, `src/percpu.go`, `src/lapic_timer.go`, `src/ipi.go`
- symbols:
  - SMP: `smpInit`, `apEntry`, `numCoresOnline`
  - percpu: `PerCPU`, `percpuInitBSPEarly`, `percpuInitAP`, `percpuLatchAPICIDCurrent`
  - timer: `lapicTimerCalibrate`, `lapicTimerInit`, `handleLAPICTimer`
  - IPI: `lapicSendIPI`, `lapicBroadcastIPI`, `broadcastPreemptIPI`, `gooosWakeupCPU`

## 04_scheduler_runtime_preemption.md

- files: `src/goroutine_tss.go`, `src/goroutine_irq.go`, `src/preempt_config.go`, `src/lapic_timer.go`
- symbols:
  - runtime hook: `gooosOnResume`
  - task metadata: `gInfoByTask`, `registerRing3GWithStack`
  - preempt ISR: `handlePreemptIPI`, `readInterruptDepth`, `readSyscallDepth`, `readPreemptDisable`
  - flags: `preemptEnabled`, `runPreemptProbe`, `runUserPreemptProbe`, `runSMPShellPreemptProbe`

## 05_process_elf_ring3_syscalls_signals.md

- files: `src/process.go`, `src/elf.go`, `src/userspace.go`, `src/user_signal.go`, `src/ring3_pool.go`
- symbols:
  - process lifecycle: `Process`, `ring3Wrapper`, `elfSpawn`, `processWait`, `processExit`
  - boot load: `elfLoad`
  - syscall dispatch: `syscallDispatch`
  - signals: `sysSigactionHandler`, `sysSigreturnHandler`, `maybeSignalUserPreempt`, `maybeDeliverSignal`
  - stack pool: `ring3StackAcquire`, `ring3StackRelease`

## 06_memory_vm_allocator_gc.md

- files: `src/vm.go`, `src/proc_pml4.go`, `src/process.go`, `src/target.json`, `user/target.json`
- symbols:
  - VM map: `mapPage`, `mapPageInto`, `unmapPage`, `walkAndGetPaddr`, `walkAndGetPaddrIn`
  - allocators: `allocPage`, `allocPagesContig`, `freePage`
  - process pml4: `newProcPML4`, `freeProcPML4`, `captureBootPML4`
  - process heap limit: `userHeapLimit`

## 07_filesystem_fd_shell_io.md

- files: `src/fs.go`, `src/fd.go`, `src/pipe.go`, `src/keyboard.go`, `src/process.go`, `user/cmd/sh/*`
- symbols:
  - FS: `fsCreate`, `fsWrite`, `fsRead`, `fsTask`, `fsReqCh`
  - FD: `FileDesc`, `procGetFD`, `procAllocFD`, `procClose`, `consoleStdin`, `consoleStdout`, `fileFd`
  - foreground: `setForegroundProc`, `getForegroundProc`

## 08_network_stack_driver_to_socket.md

- files:
  - driver: `src/pci.go`, `src/e1000.go`, `src/e1000_irq.go`
  - core: `src/net.go`, `src/ethernet.go`, `src/netbuf.go`, `src/netstats.go`
  - protocols: `src/arp.go`, `src/ipv4.go`, `src/icmp.go`, `src/udp.go`, `src/tcp.go`, `src/tcp_segment.go`, `src/tcp_flow.go`, `src/tcp_cc.go`, `src/tcp_rtt.go`, `src/tcp_retx.go`
  - sockets: `src/netsock.go`
- symbols:
  - init: `netInit`, `drainRxRing`, `ethernetDispatch`
  - UDP: `udpBind`, `udpBindWithChannel`, `udpHandle`, `udpSend`
  - TCP: `TCB`, `tcpListeners`, `tcbAlloc`, `tcbLookup`, `tcpClose`
  - socket ABI: `sysSocketHandler`, `sysBindHandler`, `sysSendtoHandler`, `sysRecvfromHandler`, `sysListenHandler`, `sysAcceptHandler`, `sysConnectHandler`, `sysTcpSendHandler`, `sysTcpRecvHandler`, `sysShutdownHandler`

## 09_userland_abi_and_embedded_elves.md

- files: `user/rt0.S`, `user/gooos/syscall.go`, `user/gooos/net.go`, `user/target.json`, `scripts/embed_elfs.sh`, `src/user_binaries.go`
- symbols:
  - syscall wrappers: `syscall0..syscall5`
  - network wrappers: `Socket`, `Bind`, `UDPSendTo`, `UDPRecvFromTimeout`, `TCPSocket`, `TCPConnect`, `TCPSend`, `TCPRecv`, `TCPShutdown`
  - embed pipeline: script output variable namespace `userElf_*`

## 10_test_harnesses_and_instability_map.md

- files: `scripts/test_preempt_kernel.sh`, `scripts/test_preempt_user.sh`, `scripts/test_smp_shell_preempt.sh`, plus broader `scripts/test_*.sh`
- markers/patterns:
  - kernel preempt: `preempt_probe_marker=`
  - user preempt: `userpreempt_marker=`
  - shell preempt: `^marker [0-9]+ cpu=`

## Coverage Check

All mandatory prompt categories are represented by at least one dedicated chapter and backed by concrete file/symbol anchors.
