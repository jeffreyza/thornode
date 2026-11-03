#!/bin/bash

echo "🚀 Building CREATE2 Vanity Address Grinder for Apple M3..."

# Set M3-specific optimizations with SIMD and aggressive inlining
export RUSTFLAGS="-C target-cpu=apple-m3 -C target-feature=+neon,+crypto -C opt-level=3 -C inline-threshold=1000"

# Build optimized release binary with target specification
cargo build --release --target=aarch64-apple-darwin

echo "✅ Build complete!"
echo "📍 Binary location: ./target/aarch64-apple-darwin/release/create2-grinder"
echo ""
echo "🎯 Quick test (find 0x931 pattern):"
echo "./target/release/create2-grinder -p '931' --stop-after 1"
echo ""
echo "🎯 Full DECDECDEC search:"
echo "./target/release/create2-grinder -p 'DECDECDEC'"
echo ""
echo "🎯 Multiple patterns:"
echo "./target/release/create2-grinder -p 'DECDECDEC,DEC931DEC,111DEC111,999999999,111111111'"
