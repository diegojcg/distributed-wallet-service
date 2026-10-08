# Origin and attribution

This project started as an implementation of the [Jungle Gaming Go backend challenge](https://github.com/junglegaming/backend-challenge-go). The original specification provided the wallet/wagering domain and the initial correctness, concurrency, authentication, and messaging requirements.

The specification used during development is available in the [original README at commit bdd9e70](https://github.com/junglegaming/backend-challenge-go/blob/bdd9e70b65db9b374ee070bd3a4768661a888997/README.md). The upstream commits remain in this repository's Git history.

The repository is now presented as **Distributed Wallet Service**, a proof of concept exploring those guarantees through an implementation, documented decisions, and reproducible tests. This presentation does not imply affiliation with or endorsement by Jungle Gaming.

The repository is published as [distributed-wallet-service](https://github.com/diegojcg/distributed-wallet-service). The Go module, database identities, OIDC realm/audience, queue names, and existing API identifiers are retained for compatibility. Changing the documentation language does not change the financial rules or protocol contracts.

See [architecture](../ARCHITECTURE.md), [test coverage](REQUIREMENTS.md), and [validation evidence](VALIDATION.md) for the implemented behavior and its limits.
