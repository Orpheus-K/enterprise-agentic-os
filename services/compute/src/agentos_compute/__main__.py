"""Inspect the compute package without pretending a Worker service is running."""

import argparse
from importlib.metadata import version


def main() -> None:
    parser = argparse.ArgumentParser(description="AgentOS stateless compute package")
    parser.add_argument("--version", action="version", version=version("agentos-compute"))
    parser.add_argument("--serve", action="store_true", help="requires verified Worker isolation")
    args = parser.parse_args()
    if args.serve:
        # No isolation provider is implemented. Environment flags cannot attest isolation.
        parser.error(
            "isolation_unavailable: no verified Worker isolation provider; service disabled"
        )
    parser.print_help()


if __name__ == "__main__":
    main()
