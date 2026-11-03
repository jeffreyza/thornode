# APP LAYER

## Overview

The app-layer is based on COSM-WASM.

Contract code is written in rust, the compiled to WASM bytecode with a checksum.
The bytecode can be deployed on THORChain using nominated accounts.

## App Layer Process

### Create

Create new WASM contract here. Check out the docs.

### Compile

Run the following script to deploy. It should produce the WASM bytecode and checksum.

```text
//deployment script
```

### Audit

Get the code audited and submit into `/audits`

### STAGENET

Apply to have it deployed on THORChain Stagenet. Provide:

1. The checksum
2. The link to the audit and code
3. The nominated deployer address

Once approved, deploy on stagenet.

### Integration Testing

Deploy and link your app, conduct thorough testing.

### Mainnet

Once the process is complete, apply through THORChain's ADR governace to have the code approved for mainnet.
