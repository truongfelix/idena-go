# Security policy

## Report a vulnerability in private

Please do not report a security vulnerability in a public issue, a pull request, a post or a comment.

Report it in private on GitHub: open the **Security** tab of this repository, then **Report a vulnerability**
(https://github.com/truongfelix/idena-go/security/advisories/new). Only you and the maintainer see the report. Idena
Social private messages to the maintainer's account are fine too.

Please include:

- the version or commit you found it in;
- what an attacker can do, and what they need for it (a peer, a crafted transaction or block, a file, a link...);
- the steps to reproduce it, or a test that shows it;
- a fix, if you have one.

## What happens next

- You get an answer within a few days.
- The fix is written and released first; the details stay private until then.
- Once the release is out, the report can be published, with your name if you wish.

## Scope

the Idena node (idena-go): the node: consensus, block and transaction checks, the peer-to-peer and IPFS layers, the RPC server, the VM and the contracts it runs. A report should affect Idena mainnet users.

Out of scope:

- advisories already published for a dependency (the fix is to update it, and the CI checks for them);
- known issues outside this code that are not patched yet (the operating system, the Linux kernel...);
- attacks that need control of the user's computer or phone, or of their key.

## Supported versions

The latest release, and the default branch.
