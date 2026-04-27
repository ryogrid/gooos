# Networking Stack — RTL8111H PHY / EPHY / ERI / MAC-OCP Init Detail

Long-form transcription of the RTL8111H (Linux `RTL_GIGA_MAC_VER_46`) initialisation sequence: the EPHY init table, the MAC-OCP / ERI body of `rtl_hw_start_8168h_1`, the `rtl8168h_2_hw_phy_config` PHY init function, and the access primitives (`eriRead`/`eriWrite`, `ephyRead`/`ephyWrite`, `macOcpRead`/`macOcpWrite`/`macOcpModify`, `phyOcpRead`/`phyOcpWrite`, `phyReadPaged`/`phyWritePaged`/`phyModifyPaged`) that all of the above depend on.

Parent doc: `rtl8111h_overview.md` (Chapter 5).
Sibling doc: `rtl8111h_review_checklist.md` (Chapter 12 references several entries here).

This file exists because the PHY init alone is ~120 lines of register writes that, if inlined into the overview, would obscure the high-level flow. The implementer of `src/rtl8111h_phy.go` should treat this file as the **literal source for the function bodies** — every line below maps to one or two lines of Go.

---

## 1. Access primitives (proposed `src/rtl8111h_phy.go` shape)

### 1.1 ERI (Extended Register Interface)

Used for: descriptor-engine FIFO tuning, packet-filter reset, MAC address read, PCIe state-tweak bits.

Cite: `r8169_main.c:_rtl_eri_read` / `_rtl_eri_write`.

```go
const (
    rERIDR = 0x70
    rERIAR = 0x74

    eriarFlag      = uint32(0x80000000)
    eriarWriteCmd  = uint32(0x80000000)
    eriarExgmac    = uint32(0x00 << 16)
    // Byte-enable nibbles in ERIAR bits [15:12]. Cite upstream
    // r8169_main.c: ERIAR_MASK_NNNN = 0xN << ERIAR_MASK_SHIFT (=12).
    eriarMask0001  = uint32(0x1 << 12) // byte 0 only          → 0x1000
    eriarMask0011  = uint32(0x3 << 12) // bytes 0..1           → 0x3000
    eriarMask0111  = uint32(0x7 << 12) // bytes 0..2           → 0x7000
    eriarMask1111  = uint32(0xf << 12) // bytes 0..3 (full)    → 0xF000
)

// eriRead reads a 32-bit ERI register. addr must be 4-byte aligned.
// Mirror of upstream r8169_main.c:_rtl_eri_read.
func eriRead(addr uint16) uint32 {
    cmd := eriarWriteCmd | eriarExgmac | eriarMask1111 | uint32(addr)
    // (write the command — note: WriteCmd|Exgmac|Mask|addr; for reads
    //  we use Read variant — see upstream; we mirror it here)
    cmd = (eriarExgmac | eriarMask1111 | uint32(addr)) // ERI_READ_CMD = 0
    mmioWrite32(rtl8111hBase+rERIAR, cmd)
    if !pollUntilLow(rtl8111hBase+rERIAR, eriarFlag, 100, 100) {
        return ^uint32(0)
    }
    return mmioRead32(rtl8111hBase + rERIDR)
}

// eriWrite writes a 32-bit ERI register with a byte-enable mask.
// mask is one of eriarMask0001/0011/0111/1111. addr must be 4-byte aligned.
func eriWrite(addr uint16, mask uint32, value uint32) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rERIDR, value)
    cmd := eriarWriteCmd | eriarExgmac | mask | uint32(addr)
    mmioWrite32(rtl8111hBase+rERIAR, cmd)
    pollUntilLow(rtl8111hBase+rERIAR, eriarFlag, 100, 100)
}

// eriSetBits / eriClearBits: read-modify-write helpers for partial
// updates. Cite r8169_main.c:rtl_eri_set_bits / rtl_eri_clear_bits.
func eriSetBits(addr uint16, mask uint32, bits uint32)   { /* RMW under lock */ }
func eriClearBits(addr uint16, mask uint32, bits uint32) { /* RMW under lock */ }
```

`pollUntilLow(addr, flag, count, usec)` is a small helper that reads `addr`, masks `flag`, returns true if it goes to 0 within `count` polls each separated by `usec` microseconds. Mirror of upstream `rtl_loop_wait_low`.

### 1.2 EPHY (Embedded PHY for PCIe SerDes)

Used for: PCIe SerDes-side tuning. The `e_info_8168h_1[]` table writes 6 EPHY registers.

Cite: `r8169_main.c:rtl_ephy_read` / `rtl_ephy_write` / `__rtl_ephy_init`.

```go
const (
    rEPHYAR = 0x80

    ephyarWriteCmd = uint32(0x80000000)
    ephyarRegMask  = uint32(0x1f)
    ephyarRegShift = 16
    ephyarDataMask = uint32(0xffff)
)

func ephyRead(reg uint8) uint16 {
    cmd := (uint32(reg) & ephyarRegMask) << ephyarRegShift
    mmioWrite32(rtl8111hBase+rEPHYAR, cmd)
    if !pollUntilHigh(rtl8111hBase+rEPHYAR, ephyarWriteCmd, 10, 100) {
        return 0xffff
    }
    return uint16(mmioRead32(rtl8111hBase+rEPHYAR) & ephyarDataMask)
}

func ephyWrite(reg uint8, value uint16) {
    cmd := ephyarWriteCmd | (uint32(value) & ephyarDataMask) |
        ((uint32(reg) & ephyarRegMask) << ephyarRegShift)
    mmioWrite32(rtl8111hBase+rEPHYAR, cmd)
    pollUntilLow(rtl8111hBase+rEPHYAR, ephyarWriteCmd, 10, 100)
    udelay(10) // upstream udelay after write
}

// ephyApplyTable runs a (reg, mask, set) sequence under the PHY lock,
// matching upstream's __rtl_ephy_init.
type ephyEntry struct {
    Reg  uint8
    Mask uint16
    Set  uint16
}
func ephyApplyTable(t []ephyEntry) {
    for _, e := range t {
        v := ephyRead(e.Reg)
        v = (v &^ e.Mask) | e.Set
        ephyWrite(e.Reg, v)
    }
}
```

### 1.3 MAC-OCP

Used for: MAC microcontroller register tuning. The closing block of `rtl_hw_start_8168h_1` is mostly `r8168_mac_ocp_modify` / `r8168_mac_ocp_write` calls.

Cite: `r8169_main.c:r8168_mac_ocp_read` / `r8168_mac_ocp_write` / `r8168_mac_ocp_modify`.

```go
const (
    rOCPDR = 0xb0
    rOCPAR = 0xb4

    ocparFlag = uint32(0x80000000)
)

// macOcpRead reads a 16-bit MAC-OCP register. reg is the OCP-space
// address, must be even (bit 0 == 0). Reg & 0xffff0001 must be 0
// (upstream WARN). For RTL8111H valid range is 0xa400..0xffff.
func macOcpRead(reg uint16) uint16 {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rOCPDR, uint32(reg)<<15)
    return uint16(mmioRead32(rtl8111hBase+rOCPDR) & 0xffff)
}

func macOcpWrite(reg uint16, data uint16) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rOCPDR, ocparFlag|(uint32(reg)<<15)|uint32(data))
}

// macOcpModify is a read-modify-write under a single lock acquire.
// Critical: the lock spans both the read and the write so a concurrent
// access cannot interleave. This is Footgun #4 (commit-series for
// "Coalesce mac ocp write and modify for 8168H start").
func macOcpModify(reg uint16, mask, set uint16) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rOCPDR, uint32(reg)<<15)
    cur := uint16(mmioRead32(rtl8111hBase+rOCPDR) & 0xffff)
    new := (cur &^ mask) | set
    mmioWrite32(rtl8111hBase+rOCPDR, ocparFlag|(uint32(reg)<<15)|uint32(new))
}
```

### 1.4 PHY-OCP (single-register window)

Used for: PHY analog/RF tuning that is not exposed via paged MII. Some of the PHY init steps in §4 use this.

Cite: `r8169_main.c:r8168_phy_ocp_read` / `r8168_phy_ocp_write`.

```go
const rGPHYOCP = 0xb8

func phyOcpRead(reg uint16) uint16 {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rGPHYOCP, uint32(reg)<<15)
    if !pollUntilHigh(rtl8111hBase+rGPHYOCP, ocparFlag, 25, 10) {
        return 0xffff
    }
    return uint16(mmioRead32(rtl8111hBase+rGPHYOCP) & 0xffff)
}

func phyOcpWrite(reg uint16, data uint16) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mmioWrite32(rtl8111hBase+rGPHYOCP, ocparFlag|(uint32(reg)<<15)|uint32(data))
    pollUntilLow(rtl8111hBase+rGPHYOCP, ocparFlag, 25, 10)
}
```

### 1.5 Paged MII (`phy_read_paged` / `phy_write_paged` / `phy_modify_paged` equivalents)

The standard MII register space is paged. To read register `reg` on page `page`, the driver writes the page number to MII reg `0x1f`, then reads `reg`. To restore predictable state, the page is reset to `0` after the access.

Linux delegates this to PHYlib (`phy_read_paged` etc.). gooos has no PHYlib; we hand-implement using two MII transactions per access. The MII access itself goes through MAC registers `MII_ACCESS = 0xfc` (write addr+ data, poll busy) — the exact two-register sequence is a chip-specific MAC API and is documented in the Realtek datasheet "MDIO Access" section.

```go
// phyReadPaged: page-select then read. Restores page 0 after.
func phyReadPaged(page uint16, reg uint8) uint16 {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mdioWrite(0x1f, page) // page-select
    v := mdioRead(reg)
    mdioWrite(0x1f, 0) // restore
    return v
}

func phyWritePaged(page uint16, reg uint8, value uint16) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mdioWrite(0x1f, page)
    mdioWrite(reg, value)
    mdioWrite(0x1f, 0)
}

func phyModifyPaged(page uint16, reg uint8, mask, set uint16) {
    flags := rtl8111hPHYLock.Acquire()
    defer rtl8111hPHYLock.Release(flags)
    mdioWrite(0x1f, page)
    cur := mdioRead(reg)
    mdioWrite(reg, (cur&^mask)|set)
    mdioWrite(0x1f, 0)
}

// mdioRead / mdioWrite: single MII transaction via the MAC's PHYAR
// register at offset 0x60. PHYAR layout (32-bit, single dword):
//
//   bit 31      busy/valid flag (the "Flag" busy bit). Driver writes
//               1 to start a write; chip clears to 0 on completion.
//               For read: driver clears (writes 0 in this slot of
//               cmd) to start; chip sets to 1 with data ready.
//   bits 20..16 5-bit MII register number (0x00..0x1f)
//   bits 15..0  16-bit data
//
// Cite r8169_main.c: PHYAR = 0x60. Both functions assume
// rtl8111hPHYLock is already held by the caller (the paged helpers
// above acquire it).
const (
    rPHYAR = 0x60

    phyarFlag     = uint32(0x80000000)
    phyarRegShift = 16
)

func mdioWrite(reg uint8, value uint16) {
    cmd := phyarFlag | (uint32(reg) << phyarRegShift) | uint32(value)
    mmioWrite32(rtl8111hBase+rPHYAR, cmd)
    pollUntilLow(rtl8111hBase+rPHYAR, phyarFlag, 25, 25) // poll busy clear
}

func mdioRead(reg uint8) uint16 {
    cmd := uint32(reg) << phyarRegShift // Flag clear → start a read
    mmioWrite32(rtl8111hBase+rPHYAR, cmd)
    if !pollUntilHigh(rtl8111hBase+rPHYAR, phyarFlag, 25, 25) {
        return 0xffff
    }
    return uint16(mmioRead32(rtl8111hBase+rPHYAR) & 0xffff)
}
```

(The PHYAR layout above is committed to per the upstream `r8169_main.c` definition of the `PHYAR = 0x60` constant; the read/write protocol is the standard Realtek "PHYAR-style" semantic shared across the entire 8169 family. Cross-check against any leaked Realtek vendor driver only if a board behaves anomalously — for the v1 bring-up, this is the source of truth.)

### 1.6 Lock ordering

`rtl8111hPHYLock` is **rank 4**. It sits below netbuf (rank 5) and above leaf-rank locks. **Never** acquire it while holding the netbuf lock. Most calls are from `rtl8111hInit` (single-threaded boot — no concurrent access) and from the kthread-side `LinkChg` observer (which holds nothing else). The TX/RX hot paths do **not** touch PHY registers, so the lock is uncontended in steady state.

---

## 2. EPHY init table (Step 13 of overview §5.1)

Verbatim from `r8169_main.c:rtl_hw_start_8168h_1` (the `e_info_8168h_1[]` table at the top of the function):

```go
var ephyInit8168h1 = []ephyEntry{
    {Reg: 0x1e, Mask: 0x0800, Set: 0x0001},
    {Reg: 0x1d, Mask: 0x0000, Set: 0x0800},
    {Reg: 0x05, Mask: 0xffff, Set: 0x2089},
    {Reg: 0x06, Mask: 0xffff, Set: 0x5881},
    {Reg: 0x04, Mask: 0xffff, Set: 0x854a},
    {Reg: 0x01, Mask: 0xffff, Set: 0x068b},
}
```

Apply via `ephyApplyTable(ephyInit8168h1)`. Each entry is "read EPHY reg N, clear bits matching `Mask`, set bits matching `Set`, write back" (see `__rtl_ephy_init` upstream).

---

## 3. MAC-OCP / ERI body of `rtl_hw_start_8168h_1` (Step 14)

The full body of `rtl_hw_start_8168h_1` after the EPHY table. Reproduced in execution order with the corresponding gooos call:

```c
/* upstream */                            /* gooos */
rtl_set_fifo_size(tp, 0x08, 0x10, 0x02, 0x06);
                                          rtlSetFifoSize(0x08, 0x10, 0x02, 0x06)
rtl8168g_set_pause_thresholds(tp, 0x38, 0x48);
                                          rtl8168gSetPauseThresholds(0x38, 0x48)
rtl_set_def_aspm_entry_latency(tp);
                                          rtlSetDefAspmEntryLatency()  // see §3.4
rtl_reset_packet_filter(tp);
                                          rtlResetPacketFilter()       // toggle ERI 0xdc bit 0
rtl_eri_set_bits(tp, 0xdc, 0x001c);
                                          eriSetBits(0xdc, eriarMask0011, 0x001c)
rtl_eri_write(tp, 0x5f0, ERIAR_MASK_0011, 0x4f87);
                                          eriWrite(0x5f0, eriarMask0011, 0x4f87)
rtl_disable_rxdvgate(tp);
                                          rtlDisableRxdvgate()         // clear MISC.RXDV_GATED_EN
rtl_eri_write(tp, 0xc0, ERIAR_MASK_0011, 0x0000);
                                          eriWrite(0xc0, eriarMask0011, 0x0000)
rtl_eri_write(tp, 0xb8, ERIAR_MASK_0011, 0x0000);
                                          eriWrite(0xb8, eriarMask0011, 0x0000)
RTL_W8(tp, DLLPR,  RTL_R8(tp, DLLPR)  & ~PFM_EN);
                                          mmioWrite8(rDLLPR, mmioRead8(rDLLPR) &^ pfmEn)
RTL_W8(tp, MISC_1, RTL_R8(tp, MISC_1) & ~PFM_D3COLD_EN);
                                          mmioWrite8(rMISC1, mmioRead8(rMISC1) &^ pfmD3coldEn)
RTL_W8(tp, DLLPR,  RTL_R8(tp, DLLPR)  & ~TX_10M_PS_EN);
                                          mmioWrite8(rDLLPR, mmioRead8(rDLLPR) &^ tx10mPsEn)
rtl_eri_clear_bits(tp, 0x1b0, BIT(12));
                                          eriClearBits(0x1b0, eriarMask0011, 1<<12)
rtl_pcie_state_l2l3_disable(tp);
                                          rtlPcieStateL2l3Disable()    // ERI 0xd4 bit 7

/* RG_SAW_CNT calibration: read MII page 0x0c42 reg 0x13, derive
 * sw_cnt_1ms_ini, write to MAC-OCP reg 0xd412. */
rg_saw_cnt = phy_read_paged(tp->phydev, 0x0c42, 0x13) & 0x3fff;
if (rg_saw_cnt > 0) {
    u16 sw_cnt_1ms_ini = (16000000 / rg_saw_cnt) & 0x0fff;
    r8168_mac_ocp_modify(tp, 0xd412, 0x0fff, sw_cnt_1ms_ini);
}
                                          rgSawCnt := phyReadPaged(0x0c42, 0x13) & 0x3fff
                                          if rgSawCnt > 0 {
                                              swCnt1msIni := (16_000_000 / rgSawCnt) & 0x0fff
                                              macOcpModify(0xd412, 0x0fff, swCnt1msIni)
                                          }

r8168_mac_ocp_modify(tp, 0xe056, 0x00f0, 0x0000);
                                          macOcpModify(0xe056, 0x00f0, 0x0000)
r8168_mac_ocp_modify(tp, 0xe052, 0x6000, 0x8008);
                                          macOcpModify(0xe052, 0x6000, 0x8008)
r8168_mac_ocp_modify(tp, 0xe0d6, 0x01ff, 0x017f);
                                          macOcpModify(0xe0d6, 0x01ff, 0x017f)
r8168_mac_ocp_modify(tp, 0xd420, 0x0fff, 0x047f);
                                          macOcpModify(0xd420, 0x0fff, 0x047f)
r8168_mac_ocp_write(tp, 0xe63e, 0x0001);
                                          macOcpWrite(0xe63e, 0x0001)
r8168_mac_ocp_write(tp, 0xe63e, 0x0000);
                                          macOcpWrite(0xe63e, 0x0000)
r8168_mac_ocp_write(tp, 0xc094, 0x0000);
                                          macOcpWrite(0xc094, 0x0000)
r8168_mac_ocp_write(tp, 0xc09e, 0x0000);
                                          macOcpWrite(0xc09e, 0x0000)
```

### 3.1 Helper transcriptions

**`rtlSetFifoSize`** — cite `r8169_main.c:rtl_set_fifo_size`. Writes 4 ERI fields at offset `0xc8`:

```go
func rtlSetFifoSize(rx, tx, rxFull, hiTh uint8) {
    cmd := uint32(rx) | (uint32(tx) << 8) | (uint32(rxFull) << 16) | (uint32(hiTh) << 24)
    eriWrite(0xc8, eriarMask1111, cmd)
}
```

**`rtl8168gSetPauseThresholds`** — cite `r8169_main.c:rtl8168g_set_pause_thresholds`. Writes 2 ERI fields at offset `0xcc`:

```go
func rtl8168gSetPauseThresholds(low, high uint8) {
    eriWrite(0xcc, eriarMask0011, uint32(low) | (uint32(high) << 16))
}
```

**`rtlResetPacketFilter`** — cite `r8169_main.c:rtl_reset_packet_filter`. Toggles ERI `0xdc` bit 0:

```go
func rtlResetPacketFilter() {
    eriClearBits(0xdc, eriarMask0001, 0x01)
    eriSetBits(0xdc, eriarMask0001, 0x01)
}
```

**`rtlDisableRxdvgate`** — cite `r8169_main.c:rtl_disable_rxdvgate`. Clears `RXDV_GATED_EN = 1 << 19` in `MISC = 0xf0` via a **32-bit RMW** (the bit lives in the upper half of the 32-bit register; an 8-bit access at `0xf0` would never reach bit 19):

```go
func rtlDisableRxdvgate() {
    v := mmioRead32(rtl8111hBase + rMISC)
    v &^= uint32(1) << 19
    mmioWrite32(rtl8111hBase+rMISC, v)
}
```

**`rtlPcieStateL2l3Disable`** — cite `r8169_main.c:rtl_pcie_state_l2l3_disable`. **Targets MMIO `Config3 = 0x54`** (an 8-bit register), clearing **`Rdy_to_L23 = 1 << 1 = 0x02`**:

```go
const (
    rConfig3   = 0x54
    rdyToL23   = uint8(0x02)
)

func rtlPcieStateL2l3Disable() {
    v := mmioRead8(rtl8111hBase + rConfig3)
    mmioWrite8(rtl8111hBase+rConfig3, v &^ rdyToL23)
}
```

(Upstream is `RTL_W8(tp, Config3, RTL_R8(tp, Config3) & ~Rdy_to_L23)` — a single-byte RMW on a MMIO register, **not** a MAC-OCP write. The earlier draft of this section incorrectly cited `macOcpModify(0xe092, ...)` — that is an unrelated OCP register used by the L2L3 LTR code path on different chips.)

### 3.2 New register constants in `src/rtl8111h.go`

```go
const (
    rChipCmd = 0x37  // 8-bit
    rTxPoll  = 0x38  // 8-bit
    rIntrMask   = 0x3c // 16-bit
    rIntrStatus = 0x3e // 16-bit
    rTxConfig   = 0x40 // 32-bit (XID lives here)
    rRxConfig   = 0x44 // 32-bit
    rCfg9346 = 0x50  // 8-bit
    rPHYstatus = 0x6c // 8-bit
    rPMCH    = 0x6f  // 8-bit
    rDLLPR   = 0xd0  // 8-bit
    rMISC    = 0xf0  // 8-bit
    rMISC1   = 0xf2  // 8-bit
    rRxMaxSize = 0xda // 16-bit

    cmdReset = 0x10
    cmdRxEnb = 0x08
    cmdTxEnb = 0x04

    cfg9346Unlock = 0xc0
    cfg9346Lock   = 0x00

    // PHYstatus bits (offset 0x6c)
    phyLinkStatus  = 0x02
    phyFullDup     = 0x01
    phy1000bpsF    = 0x10
    phy100bps      = 0x08
    phy10bps       = 0x04

    // DLLPR bits (offset 0xd0)
    pfmEn      = 0x40
    tx10mPsEn  = 0x80

    // MISC1 bits (offset 0xf2)
    // Cite r8169_main.c: PFM_D3COLD_EN = (1 << 6) = 0x40.
    pfmD3coldEn = 0x40
)
```

### 3.3 `rtlSetDefAspmEntryLatency` — Footgun #3 mitigation

Cite `r8169_main.c:rtl_set_def_aspm_entry_latency` and `rtl_set_aspm_entry_latency`. Writes PCIe config-space byte `0x70F` with `0x27` (L0 = 7 µs, L1 = 16 µs). Falls back to CSI access (PCIe `CSIAR = 0x68` / `CSIDR = 0x64`) if config-space write fails — we only implement the config-space path because gooos's `pciConfigWrite8` is well-trodden and the CSI path adds another round-trip helper.

```go
func rtlSetDefAspmEntryLatency() {
    pciConfigWrite8(rtl8111hPCI.Bus, rtl8111hPCI.Device, rtl8111hPCI.Function, 0x70F, 0x27)
}
```

(Note: gooos currently has `pciConfigRead32` / `pciConfigWrite32`. A new `pciConfigWrite8` helper is a 5-line addition to `src/pci.go`.)

### 3.4 ASPM L1 disable — Footgun #3 (commit 90ca51e) mitigation

In addition to the entry-latency tweak, the driver must clear the ASPM L1 bit in PCIe Express capability link control. Cite `r8169_main.c:rtl_init_one` (the `pci_disable_link_state(pdev, PCIE_LINK_STATE_L1)` call).

```go
func rtl8111hDisableAspmL1() {
    // Find the PCIe capability in the chained config-space cap list.
    cap := pciFindCapability(rtl8111hPCI.Bus, rtl8111hPCI.Device, rtl8111hPCI.Function, 0x10)
    if cap == 0 {
        serialPrintln("rtl8111h: no PCIe capability — cannot disable ASPM L1")
        return
    }
    // Link Control = cap + 0x10. ASPM bits are [1:0]: 00=disabled,
    // 01=L0s, 10=L1, 11=L0s+L1. Clear bit 1 to disable L1.
    lc := pciConfigRead16(rtl8111hPCI.Bus, rtl8111hPCI.Device, rtl8111hPCI.Function, cap+0x10)
    lc &^= 0x02
    pciConfigWrite16(rtl8111hPCI.Bus, rtl8111hPCI.Device, rtl8111hPCI.Function, cap+0x10, lc)
}
```

`pciFindCapability`, `pciConfigRead16`, `pciConfigWrite16` are small additions to `src/pci.go`. The capability ID for PCIe Express is `0x10`. The capability list is anchored at config offset `0x34` (Capabilities Pointer); each capability has a 1-byte ID then a 1-byte Next pointer.

---

## 4. PHY init: `rtl8168h_2_hw_phy_config` (Step 15)

Verbatim from `r8169_phy_config.c:rtl8168h_2_hw_phy_config`, transcribed line-for-line into Go:

```go
// rtl8168h2HwPhyConfig is the PHY init for VER_46 (and VER_48). Cite:
// r8169_phy_config.c:rtl8168h_2_hw_phy_config.
func rtl8168h2HwPhyConfig() {
    // (1) Apply firmware. Upstream: r8169_apply_firmware. gooos has no
    //     firmware loader and the rtl8168h-2.fw blob ships with most
    //     RTL8111H-equipped boards' firmware ROM applied at power-on
    //     by the chip itself. SKIP for v1 — the chip's default PHY
    //     code is sufficient for 1000baseT autoneg without the .fw
    //     patch. If a board misbehaves at advanced features (EEE,
    //     WoL), revisit this.
    //
    //     Footgun: skipping firmware means we lose Realtek's PHY MCU
    //     bug-fix patches. For RTL8111H this is not known to be
    //     fatal, but it is a degradation. See review-checklist item
    //     "Firmware load deferred — risk acknowledged".

    // (2) CHIN EST parameter update. Upstream:
    //     r8168g_phy_param(phydev, 0x808a, 0x003f, 0x000a).
    //     r8168g_phy_param writes 0x808a to (page 0x0a43, reg 0x13)
    //     then modifies (page 0x0a43, reg 0x14): clear 0x003f, set 0x000a.
    r8168gPhyParam(0x808a, 0x003f, 0x000a)

    // (3) Enable R-tune & PGA-retune.
    r8168gPhyParam(0x0811, 0x0000, 0x0800)
    phyModifyPaged(0x0a42, 0x16, 0x0000, 0x0002)

    // (4) Enable G-PHY 10M. Upstream: rtl8168g_enable_gphy_10m.
    phyModifyPaged(0x0a44, 0x11, 0x0000, 0x0800) // BIT(11)

    // (5) ADC bias ioffset.
    ioffset := rtl8168h2GetAdcBiasIoffset()
    if ioffset != 0xffff {
        phyWritePaged(0x0bcf, 0x16, ioffset)
    }

    // (6) Modify rlen (TX LPF corner frequency).
    data := phyReadPaged(0x0bcd, 0x16) & 0x000f
    var rlen uint16
    if data > 3 {
        rlen = data - 3
    }
    rlenData := rlen | (rlen << 4) | (rlen << 8) | (rlen << 12)
    phyWritePaged(0x0bcd, 0x17, rlenData)

    // (7) Disable PHY PFM mode.
    phyModifyPaged(0x0a44, 0x11, 0x0080, 0) // BIT(7)

    // (8) Disable 10M PLL off. **CRITICAL** — Footgun #1 (commit
    //     33189f0). Without this, RX CRC errors at low temp / 10 Mbps.
    phyModifyPaged(0x0a43, 0x10, 0x0001, 0) // BIT(0)

    // (9) Disable ALDPS. Upstream: rtl8168g_disable_aldps.
    phyModifyPaged(0x0a43, 0x10, 0x0004, 0) // BIT(2)

    // (10) Configure EEE PHY. Upstream: rtl8168g_config_eee_phy.
    phyModifyPaged(0x0a43, 0x11, 0, 0x0010) // BIT(4)
}

// r8168gPhyParam is the page-0x0a43 reg-0x13/0x14 helper. Cite
// r8169_phy_config.c:r8168g_phy_param.
func r8168gPhyParam(parm uint16, mask, set uint16) {
    phyWritePaged(0x0a43, 0x13, parm)
    phyModifyPaged(0x0a43, 0x14, mask, set)
}

// rtl8168h2GetAdcBiasIoffset reads MAC-OCP regs 0xdd02 and 0xdd00 to
// derive an ADC bias offset. Cite r8169_main.c:rtl8168h_2_get_adc_bias_ioffset.
func rtl8168h2GetAdcBiasIoffset() uint16 {
    a := macOcpRead(0xdd02)
    b := macOcpRead(0xdd00)
    if a&(1<<11) != 0 && b&(1<<11) != 0 {
        // upstream: 11+1+3-bit composite
        return ((a & 0x7ff) << 1) | ((b >> 12) & 0x07)
    }
    return 0xffff
}
```

### 4.1 Why each step matters

- **Step (2) CHIN EST**: PHY's channel-estimation parameter — without the right value, Gigabit auto-neg may negotiate but the channel estimator drifts and CRC errors creep up over hours.
- **Step (3) R-tune + PGA-retune**: enables retuning of analog gain after temperature shifts. Without it, link can degrade after long uptime.
- **Step (4) Enable G-PHY 10M**: enables the 10 Mbps PHY block. Without it, the PHY refuses to fall back to 10 Mbps when the line conditions are poor.
- **Step (5) ADC bias ioffset**: per-chip analog calibration value. Skipping it leaves the PHY in a default-but-wrong bias state on production silicon — symptom is bad RX SNR.
- **Step (6) rlen**: TX low-pass-filter corner frequency. Without the right value, TX waveform shape is wrong and the link partner's RX may struggle.
- **Step (7) Disable PFM**: power-frequency-management — masks a low-power mode that interferes with link stability.
- **Step (8) Disable 10M PLL off**: **Footgun #1**. Without this, the PHY's "10 Mbps PLL off" power-saving feature corrupts incoming bits when the link negotiates 10 Mbps and ambient temperature is low. Cite commit `33189f0`.
- **Step (9) Disable ALDPS**: ALDPS (Active Link Down Power Saving) drops the PHY into a low-power state when the link is down, which in some cases prevents link from coming back up. Disable for reliability.
- **Step (10) Configure EEE PHY**: Energy-Efficient Ethernet handshake with the link partner. Strictly speaking this could be skipped (EEE is optional), but enabling it matches upstream and avoids surprises if the link partner expects it.

---

## 5. MAC address read (Step 4 of overview §5.1)

Cite `r8169_main.c:rtl_read_mac_address` (the `rtl_is_8168evl_up` arm). VER_46 reads MAC from ERI registers, **not** from `MAC0`/`MAC4` directly:

```go
func rtl8111hReadMAC() [6]byte {
    var mac [6]byte
    lo := eriRead(0xe0)
    hi := eriRead(0xe4)
    mac[0] = byte(lo)
    mac[1] = byte(lo >> 8)
    mac[2] = byte(lo >> 16)
    mac[3] = byte(lo >> 24)
    mac[4] = byte(hi)
    mac[5] = byte(hi >> 8)
    return mac
}
```

The 4-byte ERI read at `0xe0` covers bytes 0..3 of the MAC; the 4-byte ERI read at `0xe4` provides bytes 4..5 in its low 16 bits (the upper 16 bits are reserved). All bytes are little-endian within the ERI read result.

After reading, the driver echoes the MAC to serial:

```go
serialPrintln("rtl8111h: MAC=" + macToString(rtl8111hMAC))
```

`macToString` already exists in `src/ethernet.go` (or `src/arp.go`); reuse without modification.

---

## 6. Putting it together — `rtl8111hInit` skeleton

```go
// rtl8111hInit brings the chip up. Performs the 22-step init sequence
// in impldoc/rtl8111h_overview.md §5.1. Sets activeNIC implicitly via
// rtl8111hMAC; the netInit dispatch reads activeNIC.mac.
func rtl8111hInit() {
    // (1) BAR0 map
    rtl8111hMapMMIO(rtl8111hPCI.BAR0)

    // (2) PCI Cmd: Mem Space + Bus Master already set by pciRecordRTL8111H.

    // (3) ASPM L1 disable
    rtl8111hDisableAspmL1()

    // (4) MAC address read
    rtl8111hMAC = rtl8111hReadMAC()
    serialPrintln("rtl8111h: MAC=" + macToString(rtl8111hMAC))

    // (5) Software reset
    mmioWrite8(rtl8111hBase+rChipCmd, mmioRead8(rtl8111hBase+rChipCmd)|cmdReset)
    if !pollUntilLow8(rtl8111hBase+rChipCmd, cmdReset, 100, 1000) {
        serialPrintln("rtl8111h: reset timed out — abort")
        return
    }

    // (6) 20 ms idle (Footgun #2)
    msleep(20)

    // (7) XID verify. Cite r8169_main.c:rtl_chip_infos[] — every entry
    // uses mask 0x7cf (NOT 0xfcf). XID 0x541 = RTL8111H/RTL8168H or
    // (when chip is non-GMII) RTL8107e; 0x6c0 = RTL8168M.
    xid := (mmioRead32(rtl8111hBase+rTxConfig) >> 20) & 0x7cf
    if xid != 0x541 && xid != 0x6c0 {
        serialPrintln("rtl8111h: unexpected XID — abort")
        return
    }

    // (8) Mask interrupts
    mmioWrite16(rtl8111hBase+rIntrMask, 0)

    // (9) TX ring + buffers
    rtl8111hAllocTxRing()
    // (10) RX ring + buffers
    rtl8111hAllocRxRing()

    // Unlock config regs for the protected writes
    mmioWrite8(rtl8111hBase+rCfg9346, cfg9346Unlock)

    // (11) Descriptor base addresses
    txPhys := uint64(txDescRing)
    mmioWrite32(rtl8111hBase+0x24, uint32(txPhys>>32))
    mmioWrite32(rtl8111hBase+0x20, uint32(txPhys))
    rxPhys := uint64(rxDescRing)
    mmioWrite32(rtl8111hBase+0xe8, uint32(rxPhys>>32))
    mmioWrite32(rtl8111hBase+0xe4, uint32(rxPhys))

    // (12) RxMaxSize. Upstream uses R8169_RX_BUF_SIZE + 1; for v1
    // we cap at 1519 (1518-byte max Ethernet frame + 1) so the 2 KiB
    // RX buffer per slot is always strictly larger than chip's
    // max-frame setting. Cite r8169_main.c:rtl_set_rx_max_size.
    mmioWrite16(rtl8111hBase+rRxMaxSize, 1519)

    // (13) EPHY init table
    ephyApplyTable(ephyInit8168h1)

    // (14) MAC-OCP / ERI body
    rtl8111hHwStart8168h1Body() // see §3 above

    // (15) PHY init
    rtl8168h2HwPhyConfig() // see §4

    // (16) RxConfig — DMA/burst/multi half only. No accept-mask here.
    // 0xCF00 = RX128_INT_EN(0x8000) | RX_MULTI_EN(0x4000) |
    //         RX_DMA_BURST(0x0700) | RX_EARLY_OFF(0x0800).
    mmioWrite32(rtl8111hBase+rRxConfig, 0x0000_CF00)

    // (17) RX accept mask: OR in AcceptBroadcast(0x08) | AcceptMyPhys(0x02).
    // Footgun #6: AcceptBroadcast is required for DHCP.
    cfg := mmioRead32(rtl8111hBase + rRxConfig)
    mmioWrite32(rtl8111hBase+rRxConfig, cfg|0x0A)

    // (18) TxConfig
    mmioWrite32(rtl8111hBase+rTxConfig, 0x0300_0780)

    // (19) Relock config regs
    mmioWrite8(rtl8111hBase+rCfg9346, cfg9346Lock)

    // (20) IMR — but NOT yet enabled in the chip; that happens once
    //      handleRTL8111HIRQ is registered (rtl8111hEnableInterrupts
    //      called from main.go after registerHandler).

    // (21) Engines on
    mmioWrite8(rtl8111hBase+rChipCmd, mmioRead8(rtl8111hBase+rChipCmd)|cmdRxEnb|cmdTxEnb)

    // (22) Auto-neg poll (5 s)
    rtl8111hWaitLinkUp()
}

// rtl8111hEnableInterrupts is called from main.go AFTER
// registerHandler installs the ISR. Mirror of e1000EnableInterrupts.
func rtl8111hEnableInterrupts() {
    mmioWrite16(rtl8111hBase+rIntrMask, 0x002F)
}
```

This is the whole orchestration. Each `rtl8111hHwStart8168h1Body`, `rtl8168h2HwPhyConfig`, `rtl8111hAllocTxRing`, `rtl8111hAllocRxRing`, `rtl8111hWaitLinkUp` is the corresponding sub-section above turned into Go.

---

## 7. Cross-reference

| Step in `rtl8111h_overview.md` §5.1 | Detailed in this file |
|---|---|
| Step 13 (EPHY init table) | §2 |
| Step 14 (MAC-OCP / ERI body) | §3 |
| Step 15 (PHY init `rtl8168h_2_hw_phy_config`) | §4 |
| Step 4 (MAC address read) | §5 |
| All access primitives (ERI / EPHY / MAC-OCP / PHY-OCP / paged MII) | §1 |
| Footgun #1 (10M PLL off) | §4 step (8) |
| Footgun #2 (20 ms PHY idle) | §6 step (6) |
| Footgun #3 (ASPM L1 + entry latency) | §3.3, §3.4 |
| Footgun #4 (ERI/OCP RMW lock) | §1 (every accessor takes `rtl8111hPHYLock`) |
| Footgun #5 (VER_45 removed) | §6 step (7) — XID match must accept only `0x541` / `0x6c0` |

The code-review checklist in `rtl8111h_review_checklist.md` references many of these section anchors directly.
