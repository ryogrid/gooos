# gooos Internal Implementation Documentation (2026-04-21)

## Scope

This directory is a code-grounded, static-review-oriented implementation reference for the current `gooos` repository state.

Primary audience: coding agents performing design and implementation consistency review.

Primary objective: precision over readability.

## Source-of-Truth Rule

All claims in this document set are grounded in source code under:

- `src/`
- `user/`
- `scripts/`
- `Makefile`

No dependency on external design notes is required for interpretation.

## Reading Order

1. `01_boot_and_kernel_init.md`
2. `02_cpu_descriptors_traps_interrupts.md`
3. `03_smp_lapic_timer_ipi.md`
4. `04_scheduler_runtime_preemption.md`
5. `05_process_elf_ring3_syscalls_signals.md`
6. `06_memory_vm_allocator_gc.md`
7. `07_filesystem_fd_shell_io.md`
8. `08_network_stack_driver_to_socket.md`
9. `09_userland_abi_and_embedded_elves.md`
10. `10_test_harnesses_and_instability_map.md`
11. `11_traceability_matrix.md`

## Kernel Topology Summary

- Boot path: Multiboot1 entry in `src/boot.S` transitions to long mode and calls TinyGo runtime entry.
- Runtime model: TinyGo scheduler (`scheduler=tasks`), cooperative by default, with kernel preemption support via BSP timer and preempt IPI path.
- Privilege split: Ring 0 kernel + Ring 3 user programs. Ring transitions use TSS (`RSP0`) and `iretq`-based entry/return paths.
- Process model: one Ring 3 process per goroutine wrapper (`ring3Wrapper`), with per-process PML4 and FD table.
- Networking: e1000 MMIO driver, ARP/IPv4/ICMP/UDP/TCP stack, userspace socket ABI.

## Global Implementation Invariants

1. ISR entry/exit is centralized in `src/isr.S`, preserving 15 GPRs and returning via `iretq`.
2. Per-CPU storage is anchored through `IA32_GS_BASE`; `%gs` offsets are ABI-critical.
3. Syscall ABI uses vector `0x80`; syscall dispatch source is `SyscallFrame` populated by ISR stub layout.
4. Ring 3 preempt signal injection rewrites interrupted `iretq` frame only through kernel-controlled delivery paths.
5. Kernel/user pointer dereference boundaries are enforced in syscall copy paths (user region begins at `0x40000000`).

## Non-Goals (Current State)

- No persistent filesystem backing store.
- No full POSIX signal model (implemented path is focused on SIGALRM preemption delivery).
- No AP LAPIC timer enablement in steady-state boot path (BSP timer + IPI fanout is used).

## Review Conventions Used In This Set

- File and symbol references are explicit and concrete.
- Every subsystem chapter includes invariants and edge/failure cases.
- Ambiguous behavior is marked explicitly as uncertain or unstable.
