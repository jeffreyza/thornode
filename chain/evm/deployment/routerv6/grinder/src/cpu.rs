use crossbeam_channel::Sender;
use rand::{rngs::StdRng, RngCore, SeedableRng};
use std::{
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc,
    },
    thread,
    time::Instant,
};
use tiny_keccak::keccakf;

use crate::{Found, Pattern};

// Keccak-256: rate = 136 bytes => 17 lanes (8 bytes each)
#[inline(always)]
fn le_load8(x: &[u8]) -> u64 {
    let mut buf = [0u8; 8];
    buf[..x.len()].copy_from_slice(x);
    u64::from_le_bytes(buf)
}

#[derive(Clone)]
struct Create2Midstate {
    a0: [u64; 25], // lanes with all constants & padding applied
}

impl Create2Midstate {
    fn new(factory20: [u8; 20], init_code_hash32: [u8; 32]) -> Self {
        let mut a = [0u64; 25];

        // Message layout: 0xFF (1) + factory (20) + salt (32) + init_code_hash (32) = 85 bytes
        // Keccak padding: 0x01 at byte 85, then zeros, then 0x80 at byte 135 (rate - 1)
        
        // lane 0: bytes 0..7 = [0xFF, factory[0..6]]
        {
            let mut b = [0u8; 8];
            b[0] = 0xFF;
            b[1..8].copy_from_slice(&factory20[0..7]);
            a[0] ^= u64::from_le_bytes(b);
        }
        // lane 1: bytes 8..15 = factory[7..14]
        a[1] ^= le_load8(&factory20[7..15]);
        
        // lane 2: bytes 16..23 = factory[15..19] + salt[0..3] (salt part added per-iteration)
        {
            let mut b = [0u8; 8];
            b[0..5].copy_from_slice(&factory20[15..20]);
            // salt[0..3] will be XORed in later at positions 5,6,7
            a[2] ^= u64::from_le_bytes(b);
        }

        // lanes 3,4,5 are salt-only (set per-iteration)

        // lane 6: bytes 48..55 = salt[27..31] + init_code_hash[0..3]
        {
            let mut b = [0u8; 8];
            // salt[27..31] will be XORed in later at positions 0..4
            b[5..8].copy_from_slice(&init_code_hash32[0..3]);
            a[6] ^= u64::from_le_bytes(b);
        }
        // lane 7: bytes 56..63 = init_code_hash[3..10]
        a[7] ^= le_load8(&init_code_hash32[3..11]);
        // lane 8: bytes 64..71 = init_code_hash[11..18]
        a[8] ^= le_load8(&init_code_hash32[11..19]);
        // lane 9: bytes 72..79 = init_code_hash[19..26]
        a[9] ^= le_load8(&init_code_hash32[19..27]);
        // lane 10: bytes 80..87 = init_code_hash[27..31] + 0x01 padding at byte 85
        {
            let mut b = [0u8; 8];
            b[0..5].copy_from_slice(&init_code_hash32[27..32]);
            b[5] = 0x01; // Keccak domain separation
            a[10] ^= u64::from_le_bytes(b);
        }
        // final padding bit at byte 135 (lane 16, byte offset 7)
        a[16] ^= 0x80_00_00_00_00_00_00_00u64;

        Self { a0: a }
    }

    #[inline(always)]
    fn addr_for_salt(&self, salt32: &[u8; 32]) -> [u8; 20] {
        let mut a = self.a0;

        // XOR salt bytes into lanes (little-endian within lanes)
        // lane 2: bytes 21..23 = salt[0..2] at offsets 5,6,7
        a[2] ^= ((salt32[0] as u64) << 40)
             | ((salt32[1] as u64) << 48)
             | ((salt32[2] as u64) << 56);
        // lane 3: bytes 24..31 = salt[3..10]
        a[3] ^= le_load8(&salt32[3..11]);
        // lane 4: bytes 32..39 = salt[11..18]
        a[4] ^= le_load8(&salt32[11..19]);
        // lane 5: bytes 40..47 = salt[19..26]
        a[5] ^= le_load8(&salt32[19..27]);
        // lane 6: bytes 48..52 = salt[27..31] at offsets 0..4 (low 5 bytes)
        {
            let v = (salt32[27] as u64)
                  | ((salt32[28] as u64) << 8)
                  | ((salt32[29] as u64) << 16)
                  | ((salt32[30] as u64) << 24)
                  | ((salt32[31] as u64) << 32);
            a[6] ^= v;
        }

        // Single permutation
        keccakf(&mut a);

        // Squeeze first 32 bytes (lanes 0..3) and return last 20 bytes
        let mut out32 = [0u8; 32];
        out32[0..8].copy_from_slice(&a[0].to_le_bytes());
        out32[8..16].copy_from_slice(&a[1].to_le_bytes());
        out32[16..24].copy_from_slice(&a[2].to_le_bytes());
        out32[24..32].copy_from_slice(&a[3].to_le_bytes());

        let mut addr = [0u8; 20];
        addr.copy_from_slice(&out32[12..32]);
        addr
    }
}

pub struct CpuGrinder {
    factory20: [u8; 20],
    bytecode_hash32: [u8; 32],
    patterns: Vec<Pattern>,
    threads: usize,
    fixed_salt: Option<[u8; 32]>,
}

impl CpuGrinder {
    pub fn new(
        factory20: [u8; 20],
        bytecode_hash32: [u8; 32],
        patterns: Vec<Pattern>,
        threads: usize,
        fixed_salt: Option<[u8; 32]>,
    ) -> Self {
        Self {
            factory20,
            bytecode_hash32,
            patterns,
            threads,
            fixed_salt,
        }
    }

    pub fn run(
        &self,
        attempts: Arc<AtomicU64>,
        stop: Arc<AtomicBool>,
        found_flags: Arc<Vec<AtomicBool>>,
        tx: Sender<Found>,
        start: Instant,
    ) {
        // Spawn worker threads
        for tid in 0..self.threads {
            let factory20 = self.factory20;
            let bytecode_hash32 = self.bytecode_hash32;
            let pats = self.patterns.clone();
            let attempts = attempts.clone();
            let stop = stop.clone();
            let flags = found_flags.clone();
            let tx = tx.clone();
            let fixed_salt = self.fixed_salt;

            thread::spawn(move || {
                Self::worker_thread(
                    tid,
                    factory20,
                    bytecode_hash32,
                    pats,
                    attempts,
                    stop,
                    flags,
                    tx,
                    start,
                    fixed_salt,
                );
            });
        }
    }

    fn worker_thread(
        tid: usize,
        factory20: [u8; 20],
        bytecode_hash32: [u8; 32],
        pats: Vec<Pattern>,
        attempts: Arc<AtomicU64>,
        stop: Arc<AtomicBool>,
        flags: Arc<Vec<AtomicBool>>,
        tx: Sender<Found>,
        start: Instant,
        fixed_salt: Option<[u8; 32]>,
    ) {
        let mut salt = [0u8; 32];
        let mut ctr: u128 = 0;

        if let Some(fixed) = fixed_salt {
            // Use fixed salt with thread ID offset
            salt.copy_from_slice(&fixed);
            // Add thread ID to avoid collision between threads
            let tid_bytes = (tid as u64).to_be_bytes();
            for i in 0..8 {
                salt[i] = salt[i].wrapping_add(tid_bytes[i]);
            }
        } else {
            // Deterministic salt stream per thread: [tid | random 64b | counter 128b]
            let mut rng = StdRng::seed_from_u64(
                (tid as u64)
                    ^ (Instant::now().elapsed().as_nanos() as u64)
                    ^ 0x9E3779B97F4A7C15u64,
            );
            salt[0..8].copy_from_slice(&(tid as u64).to_be_bytes());
            salt[8..16].copy_from_slice(&rng.next_u64().to_be_bytes());
        }

        // Precompute midstate once per thread - this eliminates hasher cloning!
        let mid = Create2Midstate::new(factory20, bytecode_hash32);

        let mut local_attempts: u64 = 0;
        let mut buf_ctr = [0u8; 16];
        const BATCH_SIZE: usize = 4096; // Increased batch size to amortize atomics

        while !stop.load(Ordering::Relaxed) {
            // Process a batch of salts for better cache locality
            for i in 0..BATCH_SIZE {
                // Move stop check out of inner loop for better performance
                if i % 256 == 0 && stop.load(Ordering::Relaxed) {
                    break;
                }

                // Set counter
                buf_ctr.copy_from_slice(&ctr.to_be_bytes());
                salt[16..32].copy_from_slice(&buf_ctr);
                ctr = ctr.wrapping_add(1);

                // Use fast midstate CREATE2 computation - no cloning, no heap allocation
                let addr = mid.addr_for_salt(&salt);

                // Optimized pattern check
                for p in &pats {
                    if flags[p.idx].load(Ordering::Relaxed) {
                        continue;
                    }
                    if starts_with_nibbles_optimized(&addr, &p.nibbles) {
                        // Claim pattern
                        if flags[p.idx]
                            .compare_exchange(false, true, Ordering::SeqCst, Ordering::Relaxed)
                            .is_ok()
                        {
                            let total_attempts = attempts.load(Ordering::Relaxed) + local_attempts + 1;
                            let elapsed = start.elapsed().as_secs_f64();
                            let found = Found {
                                pattern_idx: p.idx,
                                address20: addr,
                                salt32: salt,
                                attempts: total_attempts,
                                elapsed,
                            };
                            let _ = tx.send(found);
                            // Early exit if we found something
                            break;
                        }
                    }
                }

                local_attempts += 1;
            }

            // Update global counter less frequently for better performance
            attempts.fetch_add(local_attempts, Ordering::Relaxed);
            local_attempts = 0;
        }

        if local_attempts > 0 {
            attempts.fetch_add(local_attempts, Ordering::Relaxed);
        }
    }
}

fn starts_with_nibbles_optimized(addr20: &[u8; 20], nibs: &[u8]) -> bool {
    // Optimized nibble comparison with manual unrolling for common cases
    match nibs.len() {
        // Fast path for common pattern lengths with branch-free comparisons
        1 => (addr20[0] >> 4) == nibs[0],
        2 => {
            let b0 = addr20[0];
            (b0 >> 4) == nibs[0] && (b0 & 0x0F) == nibs[1]
        }
        3 => {
            let b0 = addr20[0];
            let b1 = addr20[1];
            (b0 >> 4) == nibs[0] && (b0 & 0x0F) == nibs[1] && (b1 >> 4) == nibs[2]
        }
        4 => {
            let b0 = addr20[0];
            let b1 = addr20[1];
            (b0 >> 4) == nibs[0]
                && (b0 & 0x0F) == nibs[1]
                && (b1 >> 4) == nibs[2]
                && (b1 & 0x0F) == nibs[3]
        }
        5 => {
            let b0 = addr20[0];
            let b1 = addr20[1];
            let b2 = addr20[2];
            (b0 >> 4) == nibs[0]
                && (b0 & 0x0F) == nibs[1]
                && (b1 >> 4) == nibs[2]
                && (b1 & 0x0F) == nibs[3]
                && (b2 >> 4) == nibs[4]
        }
        6 => {
            let b0 = addr20[0];
            let b1 = addr20[1];
            let b2 = addr20[2];
            (b0 >> 4) == nibs[0]
                && (b0 & 0x0F) == nibs[1]
                && (b1 >> 4) == nibs[2]
                && (b1 & 0x0F) == nibs[3]
                && (b2 >> 4) == nibs[4]
                && (b2 & 0x0F) == nibs[5]
        }
        8 => {
            // Optimized 8-nibble (4-byte) comparison using u32
            let addr_u32 = u32::from_be_bytes([addr20[0], addr20[1], addr20[2], addr20[3]]);
            let pattern_u32 = (nibs[0] as u32) << 28
                | (nibs[1] as u32) << 24
                | (nibs[2] as u32) << 20
                | (nibs[3] as u32) << 16
                | (nibs[4] as u32) << 12
                | (nibs[5] as u32) << 8
                | (nibs[6] as u32) << 4
                | (nibs[7] as u32);
            addr_u32 == pattern_u32
        }
        // General case for longer patterns - use memcmp-style comparison
        _ => {
            // Process full bytes first (pairs of nibbles)
            let full_bytes = nibs.len() / 2;
            for i in 0..full_bytes {
                let expected = (nibs[i * 2] << 4) | nibs[i * 2 + 1];
                if addr20[i] != expected {
                    return false;
                }
            }
            // Handle remaining odd nibble if any
            if nibs.len() % 2 == 1 {
                let last_idx = nibs.len() - 1;
                let b = addr20[last_idx / 2];
                let nib = b >> 4;
                if nib != nibs[last_idx] {
                    return false;
                }
            }
            true
        }
    }
}
