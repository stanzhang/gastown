# FirstMate bridge

`gt firstmate delegate` sends one existing bead to a FirstMate second mate by
calling FirstMate's tracked `bin/fm-send.sh`. Gas Town does not implement a
second SSH transport, copy credentials, close the bead, or infer remote
completion.

Preview a request without changing FirstMate or remote state:

```sh
gt firstmate delegate gt-abc alienware-ml \
  --firstmate-root /absolute/path/to/firstmate \
  --dry-run
```

The command resolves the bead with `bd show --json` and prints the normalized
`fm-alienware-ml` target plus the complete bounded payload. Add operator context
with `--instructions`.

For a live send, the FirstMate root is resolved from `--firstmate-root`,
`FIRSTMATE_ROOT`, or `FM_ROOT_OVERRIDE`. The FirstMate home is resolved from
`--firstmate-home`, `FM_HOME`, or the resolved root. The fixed
`bin/fm-send.sh` path must be executable and tracked by that checkout.

FirstMate's exit status remains authoritative: exit 0 confirms that the request
was durably recorded, but does not prove remote completion. Any nonzero exit is
propagated unchanged. In particular, SSH exit 255 means delivery is unknown;
the bridge preserves FirstMate's exact stderr and correlation-preserving
recovery command. Do not replace that command with a plain resend.
