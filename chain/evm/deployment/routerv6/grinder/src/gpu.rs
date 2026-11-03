use metal::*;

#[repr(C)]
struct Runtime {
    base_counter: u64,
    pattern_count: u32,
    max_hits: u32,
    grid_size: u32,
    iters_per_thread: u32,
    _pad0: u32,
    _pad1: u32,
}

#[repr(C)]
struct PatternDesc {
    offset: u32,
    length: u32,
}

#[repr(C)]
#[derive(Clone, Copy)]
pub struct Hit {
    pub pattern_idx: u32,
    pub addr: [u8; 20],
    pub salt: [u8; 32],
}

pub struct MetalGrinder {
    device: Device,
    command_queue: CommandQueue,
    pipeline: ComputePipelineState,
    buffer_factory: Buffer,
    buffer_bytecode_hash: Buffer,
    buffer_runtime: Buffer,
    buffer_patterns: Buffer,
    buffer_nibble_pool: Buffer,
    buffer_hit_count: Buffer,
    buffer_hits: Buffer,
    max_hits: u32,
    thread_execution_width: u64,
}

impl MetalGrinder {
    pub fn new(factory: &[u8; 20], bytecode_hash: &[u8; 32]) -> Result<Self, Box<dyn std::error::Error>> {
        let device = Device::system_default().ok_or("No Metal device found")?;
        println!("Using Metal device: {}", device.name());
        
        let command_queue = device.new_command_queue();
        
        // Compile optimized shader
        let shader_source = include_str!("gpu.metal");
        let compile_options = CompileOptions::new();
        compile_options.set_fast_math_enabled(true);
        compile_options.set_language_version(MTLLanguageVersion::V3_1);
        
        let library = device.new_library_with_source(shader_source, &compile_options)
            .map_err(|e| format!("Failed to compile shader: {}", e))?;
        
        let kernel_function = library.get_function("compute_create2_ultra", None)
            .map_err(|e| format!("Failed to get kernel: {}", e))?;
        
        let pipeline = device.new_compute_pipeline_state_with_function(&kernel_function)
            .map_err(|e| format!("Failed to create pipeline: {}", e))?;
        
        let thread_execution_width = pipeline.thread_execution_width();
        println!("Thread execution width: {}", thread_execution_width);
        println!("Max total threads per threadgroup: {}", pipeline.max_total_threads_per_threadgroup());
        
        // Create constant buffers
        let buffer_factory = device.new_buffer_with_data(
            factory.as_ptr() as *const _,
            20,
            MTLResourceOptions::StorageModeShared,
        );
        
        let buffer_bytecode_hash = device.new_buffer_with_data(
            bytecode_hash.as_ptr() as *const _,
            32,
            MTLResourceOptions::StorageModeShared,
        );
        
        // Create runtime buffer
        let buffer_runtime = device.new_buffer(
            std::mem::size_of::<Runtime>() as u64,
            MTLResourceOptions::StorageModeShared,
        );
        
        // Pattern description buffer
        let buffer_patterns = device.new_buffer(
            (8 * std::mem::size_of::<PatternDesc>()) as u64,
            MTLResourceOptions::StorageModeShared,
        );
        
        // Nibble pool buffer for pattern storage
        let max_patterns = 8u64;
        let max_nibbles_per_pattern = 64u64;
        let buffer_nibble_pool = device.new_buffer(
            max_patterns * max_nibbles_per_pattern,
            MTLResourceOptions::StorageModeShared,
        );
        
        // Atomic hit counter buffer
        let buffer_hit_count = device.new_buffer(
            4, // Single u32
            MTLResourceOptions::StorageModeShared,
        );
        
        // Hit results buffer
        let max_hits = 8192u32;
        let buffer_hits = device.new_buffer(
            (max_hits as u64) * std::mem::size_of::<Hit>() as u64,
            MTLResourceOptions::StorageModeShared,
        );
        
        Ok(MetalGrinder {
            device,
            command_queue,
            pipeline,
            buffer_factory,
            buffer_bytecode_hash,
            buffer_runtime,
            buffer_patterns,
            buffer_nibble_pool,
            buffer_hit_count,
            buffer_hits,
            max_hits,
            thread_execution_width,
        })
    }
    
    pub fn setup_patterns(&self, patterns: &[Vec<u8>]) -> Result<(), Box<dyn std::error::Error>> {
        let max_patterns = 8;
        let max_nibbles_per_pattern = 64; // supports 32 hex chars per pattern
        let mut pattern_descs = Vec::with_capacity(max_patterns);
        let mut nibble_pool = Vec::new();
        
        // Build pattern descriptors and nibble pool
        for pattern in patterns.iter().take(max_patterns) {
            // Enforce maximum pattern length to prevent buffer overflow
            let pattern_len = pattern.len().min(max_nibbles_per_pattern);
            if pattern.len() > max_nibbles_per_pattern {
                eprintln!("Warning: Pattern truncated to {} nibbles (max supported)", max_nibbles_per_pattern);
            }
            

            
            pattern_descs.push(PatternDesc {
                offset: nibble_pool.len() as u32,
                length: pattern_len as u32,
            });
            nibble_pool.extend_from_slice(&pattern[..pattern_len]);
        }
        
        // Pad to max patterns
        while pattern_descs.len() < max_patterns {
            pattern_descs.push(PatternDesc { offset: 0, length: 0 });
        }
        
        // Update buffers
        let patterns_ptr = self.buffer_patterns.contents() as *mut PatternDesc;
        let nibbles_ptr = self.buffer_nibble_pool.contents() as *mut u8;
        
        unsafe {
            std::ptr::copy_nonoverlapping(pattern_descs.as_ptr(), patterns_ptr, max_patterns);
            std::ptr::copy_nonoverlapping(nibble_pool.as_ptr(), nibbles_ptr, nibble_pool.len());
        }
        
        Ok(())
    }
    
    pub fn get_performance_info(&self) -> String {
        format!(
            "Device: {} | Thread Width: {} | Max Threads: {}",
            self.device.name(),
            self.thread_execution_width,
            self.pipeline.max_total_threads_per_threadgroup()
        )
    }
    
    pub fn get_gpu_info(&self, grid_size: usize, iters_per_thread: u32) -> crate::GpuInfo {
        let threadgroup_size = self.pipeline.max_total_threads_per_threadgroup() as u64;
        crate::GpuInfo {
            device_name: self.device.name().to_string(),
            thread_execution_width: self.thread_execution_width,
            max_threads_per_threadgroup: self.pipeline.max_total_threads_per_threadgroup() as u64,
            threadgroup_size_used: threadgroup_size,
            grid_size,
            iters_per_thread,
            attempts_per_dispatch: (grid_size as u64) * (iters_per_thread as u64),
        }
    }
    
    pub fn grind_batch(
        &self,
        start_counter: u64,
        grid_size: usize,
        iters_per_thread: u32,
        pattern_count: u32,
    ) -> Result<Vec<Hit>, Box<dyn std::error::Error>> {
        // Reset hit count
        unsafe {
            *(self.buffer_hit_count.contents() as *mut u32) = 0;
        }
        
        // Setup runtime parameters
        let runtime = Runtime {
            base_counter: start_counter,
            pattern_count,
            max_hits: self.max_hits,
            grid_size: grid_size as u32,
            iters_per_thread,
            _pad0: 0,
            _pad1: 0,
        };
        
        let runtime_ptr = self.buffer_runtime.contents() as *mut Runtime;
        unsafe {
            *runtime_ptr = runtime;
        }
        
        // Create command buffer and encoder
        let command_buffer = self.command_queue.new_command_buffer();
        let encoder = command_buffer.new_compute_command_encoder();
        
        // Set pipeline and buffers
        encoder.set_compute_pipeline_state(&self.pipeline);
        encoder.set_buffer(0, Some(&self.buffer_runtime), 0);
        encoder.set_buffer(1, Some(&self.buffer_factory), 0);
        encoder.set_buffer(2, Some(&self.buffer_bytecode_hash), 0);
        encoder.set_buffer(3, Some(&self.buffer_patterns), 0);
        encoder.set_buffer(4, Some(&self.buffer_nibble_pool), 0);
        encoder.set_buffer(5, Some(&self.buffer_hit_count), 0);
        encoder.set_buffer(6, Some(&self.buffer_hits), 0);
        
        // Configure threadgroup sizing for optimal GPU occupancy
        let threads_per_group = self.pipeline.max_total_threads_per_threadgroup() as u64;
        let total_threads = MTLSize::new(grid_size as u64, 1, 1);
        let threadgroup_size = MTLSize::new(threads_per_group, 1, 1);
        

        
        // Dispatch with exact thread coverage
        encoder.dispatch_threads(total_threads, threadgroup_size);
        encoder.end_encoding();
        
        // Submit and wait
        command_buffer.commit();
        command_buffer.wait_until_completed();
        
        if command_buffer.status() != MTLCommandBufferStatus::Completed {
            return Err(format!("Metal command execution failed: {:?}", command_buffer.status()).into());
        }
        
        // Read back hits
        let hit_count = unsafe { *(self.buffer_hit_count.contents() as *const u32) } as usize;
        let hits_ptr = self.buffer_hits.contents() as *const Hit;
        
        let mut results = Vec::with_capacity(hit_count);
        unsafe {
            for i in 0..hit_count.min(self.max_hits as usize) {
                results.push(*hits_ptr.add(i));
            }
        }
        
        Ok(results)
    }
    

}
