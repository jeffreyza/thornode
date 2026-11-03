use clap::Parser;
use crossbeam_channel::{unbounded, Receiver, Sender};
use serde::Serialize;
use std::{
    collections::HashMap,
    fs,
    path::{Path, PathBuf},
    sync::{
        atomic::{AtomicBool, AtomicU64, Ordering},
        Arc,
    },
    thread,
    time::{Duration, Instant},
};
use sha3::{Digest, Keccak256};

mod cpu;

#[cfg(target_os = "macos")]
mod gpu;

const DEFAULT_FACTORY: &str = "0x4e59b44847b379578588920cA78FbF26c0B4956C"; // Nick's

#[derive(Parser, Debug)]
#[command(name = "create2-vanity", about = "CREATE2 vanity address grinder and verifier")]
struct Args {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Parser, Debug)]
enum Commands {
    /// Grind for a vanity address matching the given pattern
    Grind {
        /// Factory address (20 bytes hex)
        #[arg(long, default_value = DEFAULT_FACTORY)]
        factory: String,

        /// Bytecode hash (keccak256 of creation bytecode), hex
        #[arg(long)]
        bytecode_hash: Option<String>,

        /// Hardhat/Foundry artifact JSON path (reads 'bytecode')
        #[arg(long)]
        artifact: Option<PathBuf>,

        /// Constructor arguments as hex (optional, appended to bytecode for CREATE2 hash)
        #[arg(long)]
        args_hex: Option<String>,

        /// Comma-separated hex patterns (no 0x), e.g. DECDEC,DEC931,DEDC
        #[arg(long, required = true)]
        patterns: String,

        /// Number of threads (default: all cores)
        #[arg(long)]
        threads: Option<usize>,

        /// Stop when any single pattern is found
        #[arg(long, default_value_t = false)]
        any: bool,

        /// Write results here (files will be created)
        #[arg(long, default_value = "results")]
        output_dir: PathBuf,

        /// Log progress every N attempts (per process)
        #[arg(long, default_value_t = 1_000_000u64)]
        progress_every: u64,

        /// Use GPU acceleration (Metal on macOS)
        #[arg(long, default_value_t = false)]
        gpu: bool,

        /// Optional salt to use instead of random generation (32 bytes hex or any string)
        #[arg(long)]
        salt: Option<String>,
    },

    /// Verify that a salt produces the expected address
    Verify {
        /// Factory address (20 bytes hex)
        #[arg(long, default_value = DEFAULT_FACTORY)]
        factory: String,

        /// Bytecode hash (keccak256 of creation bytecode), hex
        #[arg(long)]
        bytecode_hash: Option<String>,

        /// Hardhat/Foundry artifact JSON path (reads 'bytecode')
        #[arg(long)]
        artifact: Option<PathBuf>,

        /// Constructor arguments as hex (optional, appended to bytecode for CREATE2 hash)
        #[arg(long)]
        args_hex: Option<String>,

        /// Salt to verify (32 bytes hex or any string)
        #[arg(long, required = true)]
        salt: String,

        /// Expected address to verify against
        #[arg(long, required = true)]
        address: String,

        /// Use GPU acceleration (Metal on macOS)
        #[arg(long, default_value_t = false)]
        gpu: bool,
    },
}

#[derive(Clone)]
pub struct Pattern {
    pub raw: String,          // e.g. "DEC931"
    pub nibbles: Vec<u8>,     // nibble sequence to match
    pub idx: usize,           // stable index
}

#[derive(Serialize)]
struct ResultJson<'a> {
    pattern: String,       // "0xDEC931"
    address: String,       // EIP-55 checksum
    salt: String,          // 0x...
    factory: &'a str,      // 0x...
    bytecode_hash: String, // 0x... (for verification)
    attempts: u64,
    duration: f64,         // seconds
    timestamp: String,
    rate: u64,             // attempts/sec
    mode: String,          // "GPU" or "CPU"
    gpu_info: Option<GpuInfo>, // GPU-specific metrics
}

#[derive(Serialize)]
struct GpuInfo {
    device_name: String,
    thread_execution_width: u64,
    max_threads_per_threadgroup: u64,
    threadgroup_size_used: u64,
    grid_size: usize,
    iters_per_thread: u32,
    attempts_per_dispatch: u64,
}

#[derive(Debug)]
pub struct Found {
    pub pattern_idx: usize,
    pub address20: [u8; 20],
    pub salt32: [u8; 32],
    pub attempts: u64,
    pub elapsed: f64,
}

fn strip_0x(s: &str) -> &str {
    if let Some(stripped) = s.strip_prefix("0x") {
        stripped
    } else if let Some(stripped) = s.strip_prefix("0X") {
        stripped
    } else {
        s
    }
}

fn parse_hex_fixed<const N: usize>(s: &str) -> [u8; N] {
    let cleaned = strip_0x(s);
    let bytes = hex::decode(cleaned).unwrap_or_else(|e| panic!("Invalid hex '{}': {}", s, e));
    assert_eq!(bytes.len(), N, "Expected {} bytes hex, got {}", N, bytes.len());
    let mut out = [0u8; N];
    out.copy_from_slice(&bytes);
    out
}

fn parse_salt(s: &str) -> [u8; 32] {
    let cleaned = strip_0x(s);
    
    // Try to parse as hex first
    if let Ok(bytes) = hex::decode(cleaned) {
        if bytes.len() == 32 {
            let mut out = [0u8; 32];
            out.copy_from_slice(&bytes);
            return out;
        }
    }
    
    // If not valid 32-byte hex, treat as string and hash it to create a deterministic salt
    println!("Converting string '{}' to deterministic salt", s);
    keccak256(s.as_bytes())
}

fn parse_hex_vec(s: &str) -> Vec<u8> {
    hex::decode(strip_0x(s)).expect("Invalid hex")
}

fn keccak256(bytes: &[u8]) -> [u8; 32] {
    let mut hasher = Keccak256::new();
    hasher.update(bytes);
    hasher.finalize().into()
}

fn compute_create2(factory20: &[u8; 20], salt32: &[u8; 32], bytecode_hash32: &[u8; 32]) -> [u8; 20] {
    // keccak256( 0xff ++ factory ++ salt ++ bytecode_hash )[12..]
    let mut buf = [0u8; 1 + 20 + 32 + 32];
    buf[0] = 0xff;
    buf[1..21].copy_from_slice(factory20);
    buf[21..53].copy_from_slice(salt32);
    buf[53..85].copy_from_slice(bytecode_hash32);
    let h = keccak256(&buf);
    let mut out = [0u8; 20];
    out.copy_from_slice(&h[12..]);
    out
}

fn to_0x(bytes: &[u8]) -> String {
    format!("0x{}", hex::encode(bytes))
}

fn checksum_eip55(addr20: &[u8; 20]) -> String {
    // Lowercase hex without 0x
    let lower = hex::encode(addr20);
    let hash = keccak256(lower.as_bytes());
    let mut out = String::with_capacity(42);
    out.push_str("0x");
    for (i, ch) in lower.chars().enumerate() {
        if ch.is_ascii_hexdigit() && ch.is_ascii_alphabetic() {
            // pick corresponding hash nibble
            let hn = if i % 2 == 0 {
                (hash[i / 2] >> 4) & 0xF
            } else {
                hash[i / 2] & 0xF
            };
            if hn >= 8 {
                out.push(ch.to_ascii_uppercase());
            } else {
                out.push(ch);
            }
        } else {
            out.push(ch);
        }
    }
    out
}

fn pattern_to_nibbles(s: &str) -> Vec<u8> {
    let mut v = Vec::with_capacity(s.len());
    for c in s.chars() {
        let d = c.to_digit(16).unwrap_or_else(|| panic!("Pattern '{}' has non-hex char '{}'", s, c));
        v.push(d as u8);
    }
    v
}



fn expected_attempts_for(len_hex: usize) -> u128 {
    16u128.pow(len_hex as u32)
}

fn now_iso8601() -> String {
    chrono::Local::now().to_rfc3339()
}

fn sep(n: u64) -> String {
    // Add thousands separators: 1234567 -> "1,234,567"
    let s = n.to_string();
    let chars: Vec<char> = s.chars().collect();
    let mut result = String::new();
    
    for (i, &ch) in chars.iter().enumerate() {
        if i > 0 && (chars.len() - i) % 3 == 0 {
            result.push(',');
        }
        result.push(ch);
    }
    result
}

fn sep_u128(n: u128) -> String {
    // Add thousands separators for u128
    let s = n.to_string();
    let chars: Vec<char> = s.chars().collect();
    let mut result = String::new();
    
    for (i, &ch) in chars.iter().enumerate() {
        if i > 0 && (chars.len() - i) % 3 == 0 {
            result.push(',');
        }
        result.push(ch);
    }
    result
}

fn load_bytecode_hash(bytecode_hash: &Option<String>, artifact: &Option<PathBuf>, args_hex: &Option<String>) -> [u8; 32] {
    if let Some(h) = bytecode_hash {
        return parse_hex_fixed::<32>(h);
    }
    if let Some(path) = artifact {
        let s = fs::read_to_string(path).expect("Failed to read artifact file");
        let v: serde_json::Value =
            serde_json::from_str(&s).expect("Artifact is not valid JSON");
        let bc = v
            .get("bytecode")
            .and_then(|x| x.as_str())
            .expect("Artifact missing 'bytecode' string");
        
        // Parse bytecode and optionally append constructor arguments
        let mut bytecode = parse_hex_vec(bc);
        if let Some(args_hex) = args_hex {       // new CLI option
            bytecode.extend(parse_hex_vec(args_hex));
        }
        
        return keccak256(&bytecode);
    }
    panic!("Provide --bytecode-hash or --artifact");
}

fn patterns_from_str(patterns_str: &str) -> Vec<Pattern> {
    let raw_list: Vec<String> = patterns_str
        .split(',')
        .map(|s| strip_0x(s).to_ascii_uppercase())
        .filter(|s| !s.is_empty())
        .collect();
    
    raw_list
        .into_iter()
        .enumerate()
        .map(|(idx, raw)| Pattern {
            raw: raw.clone(),
            nibbles: pattern_to_nibbles(&raw),
            idx,
        })
        .collect()
}

fn save_result(
    outdir: &Path,
    pat: &Pattern,
    factory: &str,
    bytecode_hash: &[u8; 32],
    found: &Found,
    start: Instant,
    mode: &str,
    gpu_info: Option<GpuInfo>,
) -> std::io::Result<()> {
    let addr_hex = checksum_eip55(&found.address20);
    let salt_hex = format!("0x{}", hex::encode(found.salt32));
    let bytecode_hex = format!("0x{}", hex::encode(bytecode_hash));
    let duration = start.elapsed().as_secs_f64();
    let res = ResultJson {
        pattern: format!("0x{}", pat.raw),
        address: addr_hex.clone(),
        salt: salt_hex.clone(),
        factory,
        bytecode_hash: bytecode_hex,
        attempts: found.attempts,
        duration,
        timestamp: now_iso8601(),
        rate: (found.attempts as f64 / duration) as u64,
        mode: mode.to_string(),
        gpu_info,
    };

    // Save per-pattern result file
    let fname = format!("vanity-{}-result.json", pat.raw.to_ascii_lowercase());
    let fpath = outdir.join(fname);
    fs::write(&fpath, serde_json::to_vec_pretty(&res)?)?;

    println!("Saved: {}", fpath.display());
    Ok(())
}

fn main() {
    let args = Args::parse();

    match &args.command {
        Commands::Grind {
            factory,
            bytecode_hash,
            artifact,
            args_hex,
            patterns,
            threads,
            any,
            output_dir,
            progress_every,
            gpu,
            salt,
        } => {
            run_grind_mode(
                factory,
                bytecode_hash,
                artifact,
                args_hex,
                patterns,
                *threads,
                *any,
                output_dir,
                *progress_every,
                *gpu,
                salt,
            );
        }
        Commands::Verify {
            factory,
            bytecode_hash,
            artifact,
            args_hex,
            salt,
            address,
            gpu,
        } => {
            run_verify_mode(factory, bytecode_hash, artifact, args_hex, salt, address, *gpu);
        }
    }
}

fn run_grind_mode(
    factory: &str,
    bytecode_hash: &Option<String>,
    artifact: &Option<PathBuf>,
    args_hex: &Option<String>,
    patterns: &str,
    threads: Option<usize>,
    any: bool,
    output_dir: &PathBuf,
    progress_every: u64,
    gpu: bool,
    salt: &Option<String>,
) {
    let factory20 = parse_hex_fixed::<20>(factory);
    let bytecode_hash32 = load_bytecode_hash(bytecode_hash, artifact, args_hex);
    let patterns = patterns_from_str(patterns);
    let threads = threads.unwrap_or_else(num_cpus::get);

    println!("Grinding CREATE2 vanity addresses");
    println!("Factory: {}", to_0x(&factory20));
    println!("Bytecode hash: 0x{}", hex::encode(bytecode_hash32));
    println!(
        "Patterns: {}",
        patterns
            .iter()
            .map(|p| format!("0x{}", p.raw))
            .collect::<Vec<_>>()
            .join(", ")
    );
    println!("Threads: {}", threads);
    
    // Check GPU options
    #[cfg(target_os = "macos")]
    if gpu {
        println!("GPU Mode: Enabled (Metal)");
        if let Err(e) = run_gpu_mode(factory20, bytecode_hash32, patterns, salt, output_dir, any) {
            eprintln!("GPU error: {}", e);
            std::process::exit(1);
        }
        return;
    } else {
        println!("GPU Mode: Disabled (use --gpu to enable)");
    }
    
    #[cfg(not(target_os = "macos"))]
    if gpu {
        eprintln!("Error: GPU mode only supported on macOS (Metal)");
        std::process::exit(1);
    }
    
    println!();

    // Show probability estimates
    println!("Probability Estimates:");
    for pattern in &patterns {
        let expected = expected_attempts_for(pattern.nibbles.len());
        let expected_seconds = expected as f64 / 2_000_000.0; // Estimate 2M attempts/sec
        println!("   0x{}: 1 in {} (~{:.1}s)", 
                 pattern.raw, 
                 sep_u128(expected),
                 expected_seconds);
    }
    println!();

    fs::create_dir_all(output_dir).expect("Failed to create output dir");

    // Shared state
    let attempts = Arc::new(AtomicU64::new(0));
    let stop = Arc::new(AtomicBool::new(false));
    let found_flags: Arc<Vec<AtomicBool>> = Arc::new(
        (0..patterns.len()).map(|_| AtomicBool::new(false)).collect(),
    );

    let (tx, rx): (Sender<Found>, Receiver<Found>) = unbounded();
    let start = Instant::now();

    // Interrupt handler
    {
        let stop = stop.clone();
        ctrlc::set_handler(move || {
            eprintln!("\nInterrupted. Stopping workers...");
            stop.store(true, Ordering::Relaxed);
        })
        .expect("Error setting Ctrl-C handler");
    }

    // Parse optional salt parameter
    let fixed_salt = if let Some(salt_str) = salt {
        Some(parse_salt(salt_str))
    } else {
        None
    };

    // Initialize and run CPU grinder
    let cpu_grinder = cpu::CpuGrinder::new(
        factory20,
        bytecode_hash32,
        patterns.clone(),
        threads,
        fixed_salt,
    );
    
    cpu_grinder.run(attempts.clone(), stop.clone(), found_flags.clone(), tx.clone(), start);

    drop(tx); // main holds no sender; workers own the senders

    // Progress + event loop
    let mut found_map: HashMap<usize, Found> = HashMap::new();
    let progress_every = progress_every.max(1);
    let mut last_print_attempts = 0u64;
    
    loop {
        // Non-blocking drain of founds
        while let Ok(f) = rx.try_recv() {
            let pat = &patterns[f.pattern_idx];
            let addr_cs = checksum_eip55(&f.address20);
            let salt_hex = format!("0x{}", hex::encode(f.salt32));
            println!("\nFOUND 0x{}  |  {}", pat.raw, addr_cs);
            println!("Salt: {}", salt_hex);
            println!(
                "Attempts: {}  Time: {:.2}s  Rate: {:>10}/s",
                sep(f.attempts),
                f.elapsed,
                sep((f.attempts as f64 / f.elapsed) as u64)
            );

            // luck stats
            let exp = expected_attempts_for(pat.nibbles.len());
            let luck = (exp as f64) / (f.attempts as f64);
            println!(
                "Expected: ~{}  |  Luck factor: {:.2}x {}",
                sep_u128(exp),
                luck,
                if luck > 1.0 { "lucky" } else { "unlucky" }
            );

            // Save JSON files
            if let Err(e) = save_result(output_dir, pat, &to_0x(&factory20), &bytecode_hash32, &f, start, "CPU", None) {
                eprintln!("Warning: Failed to save result: {e}");
            }

            found_map.insert(f.pattern_idx, f);

            // Check termination
            if any || found_map.len() >= patterns.len() {
                stop.store(true, Ordering::Relaxed);
                break;
            }

            // Show remaining patterns
            let remaining: Vec<String> = patterns
                .iter()
                .filter(|p| !found_map.contains_key(&p.idx))
                .map(|p| format!("0x{}", p.raw))
                .collect();
            if !remaining.is_empty() {
                println!("Remaining: {}", remaining.join(", "));
            }
            println!();
        }

        // Progress
        let cur_attempts = attempts.load(Ordering::Relaxed);
        if cur_attempts >= last_print_attempts + progress_every {
            let elapsed = start.elapsed().as_secs_f64();
            let rate = if elapsed > 0.0 { cur_attempts as f64 / elapsed } else { 0.0 };
            let found_count = found_map.len();
            
            println!(
                "Progress: {}  |  Rate: {}/s  |  Found: {}/{}  |  Elapsed: {:.1}s",
                sep(cur_attempts),
                sep(rate as u64),
                found_count,
                patterns.len(),
                elapsed
            );
            
            last_print_attempts = cur_attempts;
        }

        // Check if all workers stopped
        if stop.load(Ordering::Relaxed) {
            thread::sleep(Duration::from_millis(100)); // Let workers finish
            break;
        }

        thread::sleep(Duration::from_millis(50));
    }

    // Final summary
    let final_attempts = attempts.load(Ordering::Relaxed);
    let final_duration = start.elapsed().as_secs_f64();
    let final_rate = if final_duration > 0.0 { final_attempts as f64 / final_duration } else { 0.0 };

    println!("\nFinal Summary:");
    println!("   Total Attempts: {}", sep(final_attempts));
    println!("   Total Duration: {:.2}s", final_duration);
    println!("   Average Rate: {}/s", sep(final_rate as u64));
    println!("   Patterns Found: {}/{}", found_map.len(), patterns.len());

    println!("\nFound Patterns:");
    for (idx, found) in &found_map {
        let pattern = &patterns[*idx];
        let addr = checksum_eip55(&found.address20);
        println!("   SUCCESS 0x{}: {}", pattern.raw, addr);
    }

    if found_map.len() < patterns.len() {
        let missing: Vec<String> = patterns
            .iter()
            .filter(|p| !found_map.contains_key(&p.idx))
            .map(|p| format!("0x{}", p.raw))
            .collect();
        println!("\nMissing Patterns:");
        for pattern in missing {
            println!("   MISSING {}", pattern);
        }
    }
}

fn run_verify_mode(
    factory: &str,
    bytecode_hash: &Option<String>,
    artifact: &Option<PathBuf>,
    args_hex: &Option<String>,
    salt: &str,
    expected_address: &str,
    gpu: bool,
) {
    let factory20 = parse_hex_fixed::<20>(factory);
    let bytecode_hash32 = load_bytecode_hash(bytecode_hash, artifact, args_hex);
    let salt32 = parse_salt(salt);
    let expected_addr20 = parse_hex_fixed::<20>(expected_address);
    
    println!("Verifying CREATE2 address derivation");
    println!("Factory: {}", to_0x(&factory20));
    println!("Bytecode hash: 0x{}", hex::encode(bytecode_hash32));
    println!("Salt: {}", to_0x(&salt32));
    println!("Expected address: {}", checksum_eip55(&expected_addr20));
    
    // Check GPU options
    #[cfg(target_os = "macos")]
    if gpu {
        println!("GPU Mode: Enabled (Metal Verification)");
        match run_gpu_verify(factory20, bytecode_hash32, salt32, expected_addr20) {
            Ok(success) => {
                if success {
                    println!("GPU VERIFICATION SUCCESS: Salt produces the expected address!");
                    std::process::exit(0);
                } else {
                    println!("GPU VERIFICATION FAILED: Salt does not produce the expected address");
                    std::process::exit(1);
                }
            }
            Err(e) => {
                eprintln!("GPU verification error: {}", e);
                std::process::exit(1);
            }
        }
    } else {
        println!("GPU Mode: Disabled (use --gpu to enable)");
    }
    
    #[cfg(not(target_os = "macos"))]
    if gpu {
        eprintln!("Error: GPU mode only supported on macOS (Metal)");
        std::process::exit(1);
    }
    
    println!();
    
    // Compute the actual address using CPU
    let computed_addr20 = compute_create2(&factory20, &salt32, &bytecode_hash32);
    let computed_addr_checksum = checksum_eip55(&computed_addr20);
    
    println!("Computed address: {}", computed_addr_checksum);
    
    // Compare addresses
    if computed_addr20 == expected_addr20 {
        println!("VERIFICATION SUCCESS: Salt produces the expected address!");
        std::process::exit(0);
    } else {
        println!("VERIFICATION FAILED: Salt does not produce the expected address");
        println!("   Expected: {}", checksum_eip55(&expected_addr20));
        println!("   Computed: {}", computed_addr_checksum);
        std::process::exit(1);
    }
}

#[cfg(target_os = "macos")]
fn run_gpu_verify(
    factory20: [u8; 20],
    bytecode_hash32: [u8; 32],
    salt32: [u8; 32],
    expected_addr20: [u8; 20],
) -> Result<bool, Box<dyn std::error::Error>> {
    // For verification, GPU is overkill since we only compute one address
    // But we can still show GPU device info and use optimized computation
    use crate::gpu::MetalGrinder;
    
    println!("Initializing Metal GPU for verification...");
    
    let grinder = MetalGrinder::new(&factory20, &bytecode_hash32)?;
    println!("{} (using CPU computation for single address)", grinder.get_performance_info());
    
    // For a single address computation, CPU is actually faster than GPU setup overhead
    let computed_addr20 = compute_create2(&factory20, &salt32, &bytecode_hash32);
    Ok(computed_addr20 == expected_addr20)
}

#[cfg(target_os = "macos")]
fn run_gpu_mode(
    factory20: [u8; 20],
    bytecode_hash32: [u8; 32],
    patterns: Vec<Pattern>,
    salt: &Option<String>,
    output_dir: &PathBuf,
    any: bool,
) -> Result<(), Box<dyn std::error::Error>> {
    use crate::gpu::MetalGrinder;
    
    println!("Initializing Metal GPU acceleration...");
    
    let grinder = MetalGrinder::new(&factory20, &bytecode_hash32)?;
    println!("{}", grinder.get_performance_info());
    
    std::fs::create_dir_all(output_dir)?;
    
    let pattern_nibbles: Vec<Vec<u8>> = patterns.iter().map(|p| p.nibbles.clone()).collect();
    grinder.setup_patterns(&pattern_nibbles)?;
    
    // GPU execution parameters
    const GRID_SIZE: usize = 16_777_216; // 16M threads per dispatch
    const ITERS_PER_THREAD: u32 = 64; // Multiple iterations per thread
    
    let mut total_attempts = 0u64;
    let start_time = std::time::Instant::now();
    let mut found_patterns = std::collections::HashSet::new();
    
    // Handle optional salt parameter
    let start_counter = if let Some(salt_str) = salt {
        let salt_bytes = parse_salt(salt_str);
        // Use last 8 bytes of salt as starting point for GPU
        u64::from_be_bytes([
            salt_bytes[24], salt_bytes[25], salt_bytes[26], salt_bytes[27],
            salt_bytes[28], salt_bytes[29], salt_bytes[30], salt_bytes[31]
        ])
    } else {
        0u64
    };
    
    println!("GPU: {} threads × {} iters = {} attempts per dispatch",
        sep(GRID_SIZE as u64),
        ITERS_PER_THREAD,
        sep((GRID_SIZE as u64) * (ITERS_PER_THREAD as u64))
    );
    if salt.is_some() {
        println!("Using fixed salt starting from counter: 0x{:016x}", start_counter);
    }
    println!();
    
    loop {
        let batch_attempts = (GRID_SIZE as u64) * (ITERS_PER_THREAD as u64);
        
        // Run GPU batch  
        let hits = grinder.grind_batch(
            total_attempts + start_counter,
            GRID_SIZE,
            ITERS_PER_THREAD,
            patterns.len() as u32,
        )?;
        
        total_attempts += batch_attempts;
        
        // Process hits
        for hit in hits {
            // CPU verification safety check - recompute to catch false positives
            let addr_cpu = compute_create2(&factory20, &hit.salt, &bytecode_hash32);
            if addr_cpu != hit.addr { 
                eprintln!("GPU false positive (salt {:?})", hex::encode(hit.salt));
                continue;
            }
            
            let pattern = &patterns[hit.pattern_idx as usize];
            let pattern_key = format!("0x{}", pattern.raw);
            
            if !found_patterns.contains(&pattern_key) {
                found_patterns.insert(pattern_key.clone());
                
                let duration = start_time.elapsed();
                let rate = total_attempts as f64 / duration.as_secs_f64();
                
                println!("GPU FOUND 0x{}  |  {}", pattern.raw, to_0x(&hit.addr));
                println!("Salt: {}", to_0x(&hit.salt));
                println!("Attempts: {}  Time: {:.2}s  Rate: {:>10}/s",
                    sep(total_attempts),
                    duration.as_secs_f64(),
                    sep(rate as u64)
                );
                
                let expected = expected_attempts_for(pattern.nibbles.len());
                let luck = expected as f64 / total_attempts as f64;
                println!("Expected: ~{}  |  Luck factor: {:.2}x {}",
                    sep_u128(expected),
                    luck,
                    if luck > 1.0 { "lucky" } else { "unlucky" }
                );
                
                // Save result with GPU info
                let found = Found {
                    pattern_idx: hit.pattern_idx as usize,
                    address20: hit.addr,
                    salt32: hit.salt,
                    attempts: total_attempts,
                    elapsed: duration.as_secs_f64(),
                };
                
                let gpu_info = grinder.get_gpu_info(GRID_SIZE, ITERS_PER_THREAD);
                if let Err(e) = save_result(output_dir, pattern, &to_0x(&factory20), &bytecode_hash32, &found, start_time, "GPU", Some(gpu_info)) {
                    eprintln!("Warning: Failed to save GPU result: {}", e);
                }
                
                if any || found_patterns.len() >= patterns.len() {
                    println!("\nGPU grinding complete!");
                    return Ok(());
                }
            }
        }
        
        // Progress update - every 100M attempts for better feedback
        if total_attempts % 100_000_000 == 0 {
            let duration = start_time.elapsed();
            let rate = total_attempts as f64 / duration.as_secs_f64();
            println!("GPU Progress: {}  |  Rate: {}/s  |  Elapsed: {:.1}s",
                sep(total_attempts),
                sep(rate as u64),
                duration.as_secs_f64()
            );
        }
    }
}

