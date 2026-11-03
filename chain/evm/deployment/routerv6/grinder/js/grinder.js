const { ethers } = require("hardhat");
const crypto = require("crypto");
const fs = require("fs");

// Nick's CREATE2 Factory (exists on all major chains)
const NICKS_FACTORY = "0x4e59b44847b379578588920cA78FbF26c0B4956C";

// RUNE token address (for RouterV6 constructor)
const RUNE_ADDRESS = "0x3155BA85D5F96b2d030a4966AF206230e46849cb";

// Predefined grind targets
const GRIND_TARGETS = {
  decdec: {
    type: "prefix",
    patterns: ["DECDEC"],
    description: "Find address starting with 0xDECDEC",
  },
  routerv6: {
    type: "prefix",
    patterns: ["DECDEC", "DEC931", "111DEC", "DEC999", "DEC111"],
    description: "Find RouterV6 vanity patterns",
  },
  nines: {
    type: "progressive",
    patterns: ["9", "99", "999", "9999", "99999", "999999"],
    description: "Find addresses with consecutive 9s (progressive difficulty)",
  },
  "999-111": {
    type: "prefix-suffix",
    prefix: "999",
    suffix: "111",
    description: "Find addresses starting with 999 and ending with 111",
  },
  custom: {
    type: "prefix",
    patterns: [],
    description: "Use custom patterns provided via command line",
  },
};

// CREATE2 address calculation
function computeCreate2Address(factory, salt, bytecodeHash) {
  const create2Inputs = ["0xff", factory, salt, bytecodeHash];
  const sanitizedInputs = `0x${create2Inputs.map((i) => i.slice(2)).join("")}`;
  return ethers.getAddress(`0x${ethers.keccak256(sanitizedInputs).slice(-40)}`);
}

// Generate random salt
function generateRandomSalt() {
  return ethers.hexlify(crypto.randomBytes(32));
}

// Check if address matches target prefix
function matchesPrefix(address, prefix) {
  return address.toLowerCase().startsWith(`0x${prefix.toLowerCase()}`);
}

// Check if address matches prefix-suffix pattern
function matchesPrefixSuffix(address, prefix, suffix) {
  const hex = address.toLowerCase().slice(2); // Remove 0x
  const startsCorrect = hex.startsWith(prefix.toLowerCase());
  const endsCorrect = hex.endsWith(suffix.toLowerCase());
  return startsCorrect && endsCorrect;
}

// Count consecutive characters at start of address
function countLeadingChars(address, char) {
  const hex = address.toLowerCase().slice(2); // Remove 0x
  let count = 0;
  for (let i = 0; i < hex.length; i++) {
    if (hex[i] === char.toLowerCase()) {
      count++;
    } else {
      break;
    }
  }
  return count;
}

// Check if address matches any pattern in the list
function findMatchingPattern(address, patterns) {
  const hex = address.toLowerCase().slice(2); // Remove 0x

  for (const pattern of patterns) {
    if (hex.startsWith(pattern.toLowerCase())) {
      return pattern;
    }
  }
  return null;
}

// Save result to file
function saveResult(
  target,
  pattern,
  address,
  salt,
  attempts,
  duration,
  bytecodeHash,
) {
  const result = {
    target: target,
    pattern: pattern,
    address: address,
    salt: salt,
    factory: NICKS_FACTORY,
    bytecode_hash: bytecodeHash,
    attempts: attempts,
    duration: duration,
    timestamp: new Date().toISOString(),
    rate: Math.round(attempts / duration),
  };

  // Create results directory if it doesn't exist
  if (!fs.existsSync("results")) {
    fs.mkdirSync("results");
  }

  // Save individual result
  const filename = `results/js-${target}-${pattern.toLowerCase()}-result.json`;
  fs.writeFileSync(filename, JSON.stringify(result, null, 2));

  // Append to combined results
  const allResultsFile = "results/js-all-results.json";
  let allResults = [];
  if (fs.existsSync(allResultsFile)) {
    try {
      allResults = JSON.parse(fs.readFileSync(allResultsFile, "utf8"));
    } catch (e) {
      allResults = [];
    }
  }
  allResults.push(result);
  fs.writeFileSync(allResultsFile, JSON.stringify(allResults, null, 2));

  console.log(`💾 Saved: ${filename}`);
  console.log(`💾 Updated: ${allResultsFile}`);
}

// Grind for prefix patterns
async function grindPrefix(patterns, bytecodeHash, stopAfterFirst = false) {
  console.log(
    `🔍 Searching for patterns: ${patterns.map((p) => `0x${p}`).join(", ")}`,
  );

  let attempts = 0;
  const startTime = Date.now();
  const foundPatterns = new Set();

  while (
    foundPatterns.size < patterns.length &&
    (!stopAfterFirst || foundPatterns.size === 0)
  ) {
    attempts++;

    const salt = generateRandomSalt();
    const address = computeCreate2Address(NICKS_FACTORY, salt, bytecodeHash);

    const matchedPattern = findMatchingPattern(address, patterns);
    if (matchedPattern && !foundPatterns.has(matchedPattern)) {
      foundPatterns.add(matchedPattern);

      const duration = (Date.now() - startTime) / 1000;
      const rate = Math.round(attempts / duration);

      console.log(`\n🎉 FOUND 0x${matchedPattern}!`);
      console.log(`📍 Address: ${address}`);
      console.log(`🧂 Salt: ${salt}`);
      console.log(`📊 Attempts: ${attempts.toLocaleString()}`);
      console.log(`⏱️  Duration: ${duration.toFixed(2)}s`);
      console.log(`⚡ Rate: ${rate.toLocaleString()}/s`);

      saveResult(
        "prefix",
        matchedPattern,
        address,
        salt,
        attempts,
        duration,
        bytecodeHash,
      );

      if (stopAfterFirst) break;
    }

    // Progress update every 10k attempts
    if (attempts % 10000 === 0) {
      const duration = (Date.now() - startTime) / 1000;
      const rate = Math.round(attempts / duration);
      console.log(
        `🔄 Attempts: ${attempts.toLocaleString()} | Rate: ${rate.toLocaleString()}/s | Found: ${foundPatterns.size}/${patterns.length}`,
      );
    }
  }

  return Array.from(foundPatterns);
}

// Grind for progressive difficulty (consecutive 9s)
async function grindProgressive(bytecodeHash, maxNines = 6) {
  console.log(`🔍 Progressive grinding for consecutive 9s (up to ${maxNines})`);

  let attempts = 0;
  const startTime = Date.now();
  const results = [];
  let currentTarget = 1;

  while (currentTarget <= maxNines) {
    attempts++;

    const salt = generateRandomSalt();
    const address = computeCreate2Address(NICKS_FACTORY, salt, bytecodeHash);

    const nineCount = countLeadingChars(address, "9");
    if (nineCount >= currentTarget) {
      const duration = (Date.now() - startTime) / 1000;
      const rate = Math.round(attempts / duration);

      console.log(`\n🎉 FOUND ${nineCount} consecutive 9s!`);
      console.log(`📍 Address: ${address}`);
      console.log(`🧂 Salt: ${salt}`);
      console.log(`📊 Attempts: ${attempts.toLocaleString()}`);
      console.log(`⏱️  Duration: ${duration.toFixed(2)}s`);
      console.log(`⚡ Rate: ${rate.toLocaleString()}/s`);

      saveResult(
        "progressive",
        `${nineCount}-nines`,
        address,
        salt,
        attempts,
        duration,
        bytecodeHash,
      );
      results.push({ nineCount, address, salt, attempts, duration });

      currentTarget = nineCount + 1;
    }

    // Progress update every 10k attempts
    if (attempts % 10000 === 0) {
      const duration = (Date.now() - startTime) / 1000;
      const rate = Math.round(attempts / duration);
      console.log(
        `🔄 Attempts: ${attempts.toLocaleString()} | Rate: ${rate.toLocaleString()}/s | Target: ${currentTarget} nines`,
      );
    }
  }

  return results;
}

// Grind for prefix-suffix pattern
async function grindPrefixSuffix(prefix, suffix, bytecodeHash) {
  console.log(`🔍 Searching for pattern: 0x${prefix}...${suffix}`);

  let attempts = 0;
  const startTime = Date.now();

  while (true) {
    attempts++;

    const salt = generateRandomSalt();
    const address = computeCreate2Address(NICKS_FACTORY, salt, bytecodeHash);

    if (matchesPrefixSuffix(address, prefix, suffix)) {
      const duration = (Date.now() - startTime) / 1000;
      const rate = Math.round(attempts / duration);

      console.log(`\n🎉 FOUND 0x${prefix}...${suffix}!`);
      console.log(`📍 Address: ${address}`);
      console.log(`🧂 Salt: ${salt}`);
      console.log(`📊 Attempts: ${attempts.toLocaleString()}`);
      console.log(`⏱️  Duration: ${duration.toFixed(2)}s`);
      console.log(`⚡ Rate: ${rate.toLocaleString()}/s`);

      saveResult(
        "prefix-suffix",
        `${prefix}-${suffix}`,
        address,
        salt,
        attempts,
        duration,
        bytecodeHash,
      );
      break;
    }

    // Progress update every 10k attempts
    if (attempts % 10000 === 0) {
      const duration = (Date.now() - startTime) / 1000;
      const rate = Math.round(attempts / duration);
      console.log(
        `🔄 Attempts: ${attempts.toLocaleString()} | Rate: ${rate.toLocaleString()}/s`,
      );
    }
  }
}

// Main grinder function
async function main() {
  console.log("🎯 JavaScript CREATE2 Vanity Address Grinder");
  console.log("==============================================");

  // Parse command line arguments
  const args = process.argv.slice(2);
  const target = args[0];

  if (!target || !GRIND_TARGETS[target]) {
    console.log("\n❌ Invalid or missing target. Available targets:");
    Object.entries(GRIND_TARGETS).forEach(([key, config]) => {
      console.log(`   ${key.padEnd(12)} - ${config.description}`);
    });
    console.log("\nUsage:");
    console.log("   node grinder.js <target> [custom_patterns...]");
    console.log("\nExamples:");
    console.log("   node grinder.js decdec");
    console.log("   node grinder.js routerv6");
    console.log("   node grinder.js nines");
    console.log("   node grinder.js 999-111");
    console.log("   node grinder.js custom CAFE,BEEF,DEAD");
    process.exit(1);
  }

  console.log(`🎯 Target: ${target}`);
  console.log(`📝 Description: ${GRIND_TARGETS[target].description}`);
  console.log(`🏭 Factory: ${NICKS_FACTORY}`);

  // Get THORChain_RouterV6 contract factory
  console.log(`\n📄 Getting THORChain_RouterV6 bytecode...`);
  const RouterV6 = await ethers.getContractFactory("THORChain_Router");

  // RouterV6 has no constructor - just use the bytecode directly
  const creationBytecode = RouterV6.bytecode;
  const bytecodeHash = ethers.keccak256(creationBytecode);

  console.log(`📊 Bytecode hash: ${bytecodeHash}`);
  console.log();

  const config = GRIND_TARGETS[target];

  try {
    switch (config.type) {
      case "prefix":
        let patterns = config.patterns;
        if (target === "custom") {
          if (args.length < 2) {
            console.log(
              "❌ Custom target requires patterns. Usage: node grinder.js custom CAFE,BEEF,DEAD",
            );
            process.exit(1);
          }
          patterns = args[1].split(",").map((p) => p.trim().toUpperCase());
        }
        await grindPrefix(patterns, bytecodeHash, target === "decdec");
        break;

      case "progressive":
        await grindProgressive(bytecodeHash, 6);
        break;

      case "prefix-suffix":
        await grindPrefixSuffix(config.prefix, config.suffix, bytecodeHash);
        break;

      default:
        console.log(`❌ Unknown grind type: ${config.type}`);
        process.exit(1);
    }

    console.log("\n🏆 Grinding complete!");
  } catch (error) {
    console.error("❌ Error during grinding:", error);
    process.exit(1);
  }
}

// Handle interruption gracefully
process.on("SIGINT", () => {
  console.log("\n⏹️  Interrupted. Exiting...");
  process.exit(0);
});

// Run the grinder
if (require.main === module) {
  main().catch(console.error);
}

module.exports = {
  computeCreate2Address,
  generateRandomSalt,
  matchesPrefix,
  matchesPrefixSuffix,
  countLeadingChars,
  findMatchingPattern,
  saveResult,
  grindPrefix,
  grindProgressive,
  grindPrefixSuffix,
  GRIND_TARGETS,
  NICKS_FACTORY,
};
