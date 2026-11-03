#include <metal_stdlib>
using namespace metal;

// Keccak-f[1600] round constants
constant ulong RC[24] = {
    0x0000000000000001ul, 0x0000000000008082ul, 0x800000000000808Aul, 0x8000000080008000ul,
    0x000000000000808Bul, 0x0000000080000001ul, 0x8000000080008081ul, 0x8000000000008009ul,
    0x000000000000008Aul, 0x0000000000000088ul, 0x0000000080008009ul, 0x000000008000000Aul,
    0x000000008000808Bul, 0x800000000000008Bul, 0x8000000000008089ul, 0x8000000000008003ul,
    0x8000000000008002ul, 0x8000000000000080ul, 0x000000000000800Aul, 0x800000008000000Aul,
    0x8000000080008081ul, 0x8000000000008080ul, 0x0000000080000001ul, 0x8000000080008008ul
};

// Rho rotation offsets for Keccak permutation
constant uint RHO[25] = {
     0, 36,  3, 41, 18,  1, 44, 10, 45,  2, 62,  6, 43, 15, 61,
    28, 55, 25, 21, 56, 27, 20, 39,  8, 14
};

// 64-bit left rotation with safe handling of n=0 case
inline ulong rotl64(ulong x, uint n) {
    n &= 63u;  // Clamp rotation amount to valid range [0,63]
    return (x << n) | (x >> ((64u - n) & 63u));
}

// Keccak-f[1600] permutation implementation
inline void keccak_f1600(thread ulong A[25]) {
    ulong C[5], D[5], B[25];

    for (uint round = 0; round < 24; ++round) {
        // Theta step
        C[0] = A[0] ^ A[5] ^ A[10] ^ A[15] ^ A[20];
        C[1] = A[1] ^ A[6] ^ A[11] ^ A[16] ^ A[21];
        C[2] = A[2] ^ A[7] ^ A[12] ^ A[17] ^ A[22];
        C[3] = A[3] ^ A[8] ^ A[13] ^ A[18] ^ A[23];
        C[4] = A[4] ^ A[9] ^ A[14] ^ A[19] ^ A[24];
        
        D[0] = rotl64(C[4], 1u) ^ C[1]; D[1] = rotl64(C[0], 1u) ^ C[2]; 
        D[2] = rotl64(C[1], 1u) ^ C[3]; D[3] = rotl64(C[2], 1u) ^ C[4]; 
        D[4] = rotl64(C[3], 1u) ^ C[0];
        
        A[0] ^= D[0]; A[5] ^= D[0]; A[10] ^= D[0]; A[15] ^= D[0]; A[20] ^= D[0];
        A[1] ^= D[1]; A[6] ^= D[1]; A[11] ^= D[1]; A[16] ^= D[1]; A[21] ^= D[1];
        A[2] ^= D[2]; A[7] ^= D[2]; A[12] ^= D[2]; A[17] ^= D[2]; A[22] ^= D[2];
        A[3] ^= D[3]; A[8] ^= D[3]; A[13] ^= D[3]; A[18] ^= D[3]; A[23] ^= D[3];
        A[4] ^= D[4]; A[9] ^= D[4]; A[14] ^= D[4]; A[19] ^= D[4]; A[24] ^= D[4];

        // Rho and Pi steps
        B[0] = rotl64(A[0], RHO[0]); B[10] = rotl64(A[1], RHO[1]); B[20] = rotl64(A[2], RHO[2]); 
        B[5] = rotl64(A[3], RHO[3]); B[15] = rotl64(A[4], RHO[4]);
        B[16] = rotl64(A[5], RHO[5]); B[1] = rotl64(A[6], RHO[6]); B[11] = rotl64(A[7], RHO[7]); 
        B[21] = rotl64(A[8], RHO[8]); B[6] = rotl64(A[9], RHO[9]);
        B[7] = rotl64(A[10], RHO[10]); B[17] = rotl64(A[11], RHO[11]); B[2] = rotl64(A[12], RHO[12]); 
        B[12] = rotl64(A[13], RHO[13]); B[22] = rotl64(A[14], RHO[14]);
        B[23] = rotl64(A[15], RHO[15]); B[8] = rotl64(A[16], RHO[16]); B[18] = rotl64(A[17], RHO[17]); 
        B[3] = rotl64(A[18], RHO[18]); B[13] = rotl64(A[19], RHO[19]);
        B[14] = rotl64(A[20], RHO[20]); B[24] = rotl64(A[21], RHO[21]); B[9] = rotl64(A[22], RHO[22]); 
        B[19] = rotl64(A[23], RHO[23]); B[4] = rotl64(A[24], RHO[24]);

        // Chi step
        A[0] = B[0] ^ ((~B[1]) & B[2]); A[1] = B[1] ^ ((~B[2]) & B[3]); A[2] = B[2] ^ ((~B[3]) & B[4]); 
        A[3] = B[3] ^ ((~B[4]) & B[0]); A[4] = B[4] ^ ((~B[0]) & B[1]);
        A[5] = B[5] ^ ((~B[6]) & B[7]); A[6] = B[6] ^ ((~B[7]) & B[8]); A[7] = B[7] ^ ((~B[8]) & B[9]); 
        A[8] = B[8] ^ ((~B[9]) & B[5]); A[9] = B[9] ^ ((~B[5]) & B[6]);
        A[10] = B[10] ^ ((~B[11]) & B[12]); A[11] = B[11] ^ ((~B[12]) & B[13]); A[12] = B[12] ^ ((~B[13]) & B[14]); 
        A[13] = B[13] ^ ((~B[14]) & B[10]); A[14] = B[14] ^ ((~B[10]) & B[11]);
        A[15] = B[15] ^ ((~B[16]) & B[17]); A[16] = B[16] ^ ((~B[17]) & B[18]); A[17] = B[17] ^ ((~B[18]) & B[19]); 
        A[18] = B[18] ^ ((~B[19]) & B[15]); A[19] = B[19] ^ ((~B[15]) & B[16]);
        A[20] = B[20] ^ ((~B[21]) & B[22]); A[21] = B[21] ^ ((~B[22]) & B[23]); A[22] = B[22] ^ ((~B[23]) & B[24]); 
        A[23] = B[23] ^ ((~B[24]) & B[20]); A[24] = B[24] ^ ((~B[20]) & B[21]);

        // Iota step
        A[0] ^= RC[round];
    }
}

inline ulong load64_le(const thread uchar* p) {
    return ((ulong)p[0]) | ((ulong)p[1] << 8) | ((ulong)p[2] << 16) | ((ulong)p[3] << 24) |
           ((ulong)p[4] << 32) | ((ulong)p[5] << 40) | ((ulong)p[6] << 48) | ((ulong)p[7] << 56);
}

inline void store64_le(thread uchar* p, ulong v) {
    p[0]=(uchar)(v); p[1]=(uchar)(v>>8); p[2]=(uchar)(v>>16); p[3]=(uchar)(v>>24);
    p[4]=(uchar)(v>>32); p[5]=(uchar)(v>>40); p[6]=(uchar)(v>>48); p[7]=(uchar)(v>>56);
}

inline void keccak256(thread const uchar* msg, uint len, thread uchar out32[32]) {
    ulong S[25];
    for (uint i = 0; i < 25; ++i) S[i] = 0;
    
    const uint rate = 136;

    // Absorb full blocks
    uint off = 0;
    while (len - off >= rate) {
        for (uint i = 0; i < 17; ++i) {
            S[i] ^= load64_le(&msg[off + i*8]);
        }
        keccak_f1600(S);
        off += rate;
    }

    // Last partial block + padding
    uchar block[136];
    for (uint i = 0; i < rate; ++i) block[i] = 0;
    uint rem = len - off;
    for (uint i = 0; i < rem; ++i) block[i] = msg[off + i];
    block[rem] ^= 0x01;          // Keccak padding
    block[rate-1] ^= 0x80;

    for (uint i = 0; i < 17; ++i) {
        S[i] ^= load64_le(&block[i*8]);
    }
    keccak_f1600(S);

    // Squeeze 32 bytes
    for (uint i = 0; i < 4; ++i) {
        store64_le(&out32[i*8], S[i]);
    }
}

struct PatternDesc { 
    uint offset; 
    uint length; 
};

struct Runtime {
    ulong base_counter;
    uint  pattern_count;
    uint  max_hits;
    uint  grid_size;
    uint  iters_per_thread;
    uint  _pad0;
    uint  _pad1;
};

struct Hit {
    uint  pattern_idx;
    uchar addr[20];
    uchar salt[32];
};

// CREATE2 address mining kernel with pattern matching
kernel void compute_create2_ultra(
    constant Runtime&     rt               [[ buffer(0) ]],
    constant uchar*       factory          [[ buffer(1) ]],
    constant uchar*       bytecode_hash    [[ buffer(2) ]],
    constant PatternDesc* patterns         [[ buffer(3) ]],
    constant uchar*       nibble_pool      [[ buffer(4) ]],
    device   atomic_uint* hit_count        [[ buffer(5) ]],
    device   Hit*         hits             [[ buffer(6) ]],
    uint gid [[thread_position_in_grid]]
) {
    // Thread-local message buffer
    uchar msg[85];

    // Build constant parts of CREATE2 message (0xff || factory || salt || bytecode_hash)
    msg[0] = 0xFF;  // CREATE2 control character
    for (uint i = 0; i < 20; ++i) msg[1+i] = factory[i];        // Factory address
    for (uint i = 0; i < 32; ++i) msg[53+i] = bytecode_hash[i]; // Bytecode hash

    // Calculate thread-specific counter
    ulong ctr = rt.base_counter + (ulong)gid;
    
    // Process multiple iterations per thread for efficiency
    for (uint iter = 0; iter < rt.iters_per_thread; ++iter, ctr += (ulong)rt.grid_size) {
        uchar H[32], addr[20], salt[32];

        // Initialize salt to zero
        for (uint i = 0; i < 32; ++i) salt[i] = 0;

        // Thread identifier in first 4 bytes (big-endian)
        salt[0] = (uchar)(gid >> 24);
        salt[1] = (uchar)(gid >> 16);
        salt[2] = (uchar)(gid >> 8);
        salt[3] = (uchar)(gid);

        // Counter in last 8 bytes (big-endian)
        salt[24] = (uchar)(ctr >> 56);
        salt[25] = (uchar)(ctr >> 48);
        salt[26] = (uchar)(ctr >> 40);
        salt[27] = (uchar)(ctr >> 32);
        salt[28] = (uchar)(ctr >> 24);
        salt[29] = (uchar)(ctr >> 16);
        salt[30] = (uchar)(ctr >> 8);
        salt[31] = (uchar)(ctr);

        // Insert salt into message
        for (uint i = 0; i < 32; ++i) msg[21 + i] = salt[i];

        // Compute Keccak-256
        keccak256(msg, 85, H);
        
        // Extract address from last 20 bytes of hash
        for (uint i = 0; i < 20; ++i) addr[i] = H[32 - 20 + i];

        // Pattern matching - check all patterns efficiently
        for (uint p = 0; p < rt.pattern_count; ++p) {
            PatternDesc desc = patterns[p];
            bool matches = true;
            
            // Direct nibble comparison
            for (uint k = 0; k < desc.length && matches; ++k) {
                uint byte_idx = k / 2;
                uint nibble_in_byte = k % 2;
                
                // Extract nibble from address byte
                uint addr_nibble;
                if (nibble_in_byte == 0) {
                    addr_nibble = (addr[byte_idx] >> 4) & 0xF;  // High nibble
                } else {
                    addr_nibble = addr[byte_idx] & 0xF;         // Low nibble
                }
                
                // Get expected nibble from pattern
                uint expected_nibble = (uint)nibble_pool[desc.offset + k];
                
                if (addr_nibble != expected_nibble) {
                    matches = false;
                }
            }
            
            if (matches) {
                // Atomic reservation for hit storage
                uint slot = atomic_fetch_add_explicit(hit_count, 1u, memory_order_relaxed);
                if (slot < rt.max_hits) {
                    hits[slot].pattern_idx = p;
                    
                    // Write address
                    for (uint i = 0; i < 20; ++i) {
                        hits[slot].addr[i] = addr[i];
                    }
                    
                    // Write salt
                    for (uint i = 0; i < 32; ++i) {
                        hits[slot].salt[i] = salt[i];
                    }
                }
                break; // Found match, stop checking other patterns
            }
        }
    }
}
