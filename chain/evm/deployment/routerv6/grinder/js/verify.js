const fs = require("fs");
const path = require("path");
const { computeCreate2Address, NICKS_FACTORY } = require("./grinder");

// Verification function for a single result
function verifyResult(result) {
  console.log(`\n🔍 Verifying result for pattern: ${result.pattern}`);
  console.log(`📍 Expected address: ${result.address}`);
  console.log(`🧂 Salt: ${result.salt}`);
  console.log(`🏭 Factory: ${result.factory}`);
  console.log(`🧱 Bytecode hash: ${result.bytecode_hash}`);

  // Verify factory matches expected
  if (result.factory.toLowerCase() !== NICKS_FACTORY.toLowerCase()) {
    console.log(
      `❌ Factory mismatch! Expected: ${NICKS_FACTORY}, Got: ${result.factory}`,
    );
    return false;
  }

  // Compute CREATE2 address using standard format
  const computedAddress = computeCreate2Address(
    result.factory,
    result.salt,
    result.bytecode_hash,
  );

  console.log(`🧮 Computed address: ${computedAddress}`);

  // Compare addresses (case-insensitive)
  const addressMatch =
    computedAddress.toLowerCase() === result.address.toLowerCase();

  if (addressMatch) {
    console.log(`✅ Address verification PASSED`);

    // Verify the pattern actually matches
    const pattern = result.pattern.replace("0x", "").toLowerCase();
    const addressHex = computedAddress.toLowerCase().replace("0x", "");
    const patternMatch = addressHex.startsWith(pattern);

    if (patternMatch) {
      console.log(`✅ Pattern verification PASSED (0x${pattern})`);

      // Display performance info if available
      if (result.mode) {
        console.log(`🚀 Mode: ${result.mode}`);
        console.log(`⚡ Rate: ${result.rate?.toLocaleString()}/s`);
        console.log(`📊 Attempts: ${result.attempts?.toLocaleString()}`);
        console.log(`⏱️  Duration: ${result.duration?.toFixed(3)}s`);

        if (result.gpu_info) {
          console.log(`🖥️  GPU: ${result.gpu_info.device_name}`);
          console.log(
            `🧵 Threads: ${result.gpu_info.grid_size?.toLocaleString()} × ${result.gpu_info.iters_per_thread} = ${result.gpu_info.attempts_per_dispatch?.toLocaleString()} per dispatch`,
          );
          console.log(
            `🎯 Threadgroup: ${result.gpu_info.threadgroup_size_used}/${result.gpu_info.max_threads_per_threadgroup}`,
          );
        }
      }

      return true;
    } else {
      console.log(
        `❌ Pattern verification FAILED - address doesn't start with 0x${pattern}`,
      );
      return false;
    }
  } else {
    console.log(`❌ Address verification FAILED`);
    console.log(`   Expected: ${result.address}`);
    console.log(`   Computed: ${computedAddress}`);
    return false;
  }
}

// Verify all results in a file
function verifyResultsFile(filePath) {
  console.log(`\n📂 Verifying results file: ${filePath}`);

  if (!fs.existsSync(filePath)) {
    console.log(`❌ File not found: ${filePath}`);
    return false;
  }

  try {
    const content = fs.readFileSync(filePath, "utf8");
    const results = JSON.parse(content);

    // Handle both single result and array of results
    const resultsArray = Array.isArray(results) ? results : [results];

    console.log(`📊 Found ${resultsArray.length} result(s) to verify`);

    let allPassed = true;
    let passedCount = 0;

    for (const result of resultsArray) {
      const passed = verifyResult(result);
      if (passed) {
        passedCount++;
      } else {
        allPassed = false;
      }
    }

    console.log(`\n📊 Verification Summary:`);
    console.log(`   Passed: ${passedCount}/${resultsArray.length}`);
    console.log(`   Status: ${allPassed ? "✅ ALL PASSED" : "❌ SOME FAILED"}`);

    return allPassed;
  } catch (error) {
    console.log(`❌ Error reading/parsing file: ${error.message}`);
    return false;
  }
}

// Main verification function
function main() {
  console.log("🔍 CREATE2 Result Verification Tool");
  console.log("===================================");

  const args = process.argv.slice(2);

  if (args.length === 0) {
    console.log("\n❌ No file specified");
    console.log("\nUsage:");
    console.log("   node verify.js <result-file.json>");
    console.log("   node verify.js ../enhanced-results/vanity-abc-result.json");
    console.log(
      "   node verify.js ../enhanced-results/all-vanity-results.json",
    );
    process.exit(1);
  }

  const filePath = args[0];
  const success = verifyResultsFile(filePath);

  if (success) {
    console.log("\n🎉 All verifications passed!");
    process.exit(0);
  } else {
    console.log("\n💥 Some verifications failed!");
    process.exit(1);
  }
}

// Export for use as module
module.exports = {
  verifyResult,
  verifyResultsFile,
};

// Run if called directly
if (require.main === module) {
  main();
}
