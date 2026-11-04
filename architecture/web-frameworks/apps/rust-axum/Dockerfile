FROM rust:1.98.1-slim-trixie@sha256:4cd829461bd5c4d511c32e269da9cb8929223b666519d8004e35fc8d1d771ab7
WORKDIR /src
COPY . .
RUN cargo build --release --locked -q
ENTRYPOINT ["/src/target/release/app"]
