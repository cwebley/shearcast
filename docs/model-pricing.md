# Model pricing and usage reporting

Verified against public primary sources on **2026-09-23**. Research used
unauthenticated GET requests and documentation. It did not use an API key or
make an inference request.

## Verified Jev rate

Shearcast calls `POST https://openrouter.ai/api/alpha/decisions` with model
`typesafe/jev-1.13`.

| Component | Published USD rate |
| --- | ---: |
| Input tokens | $0.000000042 per token, or $0.042 per million |
| Output tokens | $0 |

The [single-model metadata](https://openrouter.ai/api/v1/model/typesafe/jev-1.13)
reports `"pricing":{"prompt":"0.000000042","completion":"0"}`. The
[provider endpoint metadata](https://openrouter.ai/api/v1/models/typesafe/jev-1.13/endpoints)
reports the same prices and a discount of zero. OpenRouter's
[model documentation](https://openrouter.ai/docs/guides/overview/models.md)
defines prompt pricing in USD per input token.

The endpoint-specific [Jev guide](https://openrouter.ai/docs/guides/community/jev.md)
says: "Output tokens are free. You pay per input token at the price on the model
page." [TypeSafe's model documentation](https://docs.typesafe.ai/models.md)
independently lists $42 per billion input tokens and $0.042 per million, with
free output. No per-question or fixed per-request charge is published for this
model. Shearcast does not multiply cost by the number of questions.

The [official tutorial](https://openrouter.ai/docs/guides/community/jev-tutorial.md)
includes 476 input tokens, 70 output tokens and `cost: 0.000019992`. This matches
476 times $0.000000042. It is a published example, not a measurement made here.

## Model availability

The [decision-model catalog](https://openrouter.ai/api/v1/models?output_modalities=decisions)
lists `typesafe/jev-1.13`, canonical slug `typesafe/jev-1.13-20260917`, with no
expiration date. Its endpoint metadata lists one TypeSafe endpoint. The default
model catalog returns text-output models; a missing Jev entry there is expected.

The [public model page](https://openrouter.ai/typesafe/jev-1.13) returned HTTP 404
during this check. Positive catalog metadata verifies a listing, not successful
inference. No paid availability check was performed.

## Reported versus calculated cost

The [Decisions reference](https://openrouter.ai/docs/api/api-reference/alphadecisions/submit-a-decisions-request.md)
requires `usage.input_tokens` and `usage.output_tokens` in its success schema.
It declares `usage.cost`, in USD, as optional. Although the Jev guide says every
response includes cost, Shearcast follows the schema and accepts its absence.
Missing cost is unknown, while a returned zero is known zero.

Reports distinguish:

- **Reported tokens** received from OpenRouter, including responses from work
  that subsequently fails validation, detection, rendering or publication.
- **Calculated cost**, using those tokens and a saved model-specific rate.
- **OpenRouter-reported cost**, summing returned `usage.cost` values independently.

Each calculation saves the model, input/output rates, units, source URL and
verification date. The built-in rate applies only to `typesafe/jev-1.13`.
Other configured models have unknown calculated cost unless the code supplies
an explicit rate. There is no runtime pricing lookup. A published price may
change after its verification date; the response's reported cost can differ.

These figures are model-cost observations, not an account invoice. OpenRouter's
[pricing page](https://openrouter.ai/pricing) describes credit-purchase fees,
currently 5.5% for Standard and 8% for Business, and excludes applicable taxes.
Its [general FAQ](https://openrouter.ai/docs/faq.md) also describes a minimum
standard credit-purchase fee and separate crypto terms. Shearcast does not
infer account-specific effective rates or include these charges. Hosting,
storage and bandwidth are separate.

## Failures, retries and interrupted work

Every dispatched HTTP attempt counts, including retries. Missing or invalid
usage and missing cost have separate counters. Lost, truncated or undecodable
responses leave unknown observations; a successful retry does not erase them.
Reports label known subtotals as partial when observations are missing.

OpenRouter's [pricing page](https://openrouter.ai/pricing) states that attempts
which error are not charged for model tokens. Its
[zero-completion insurance](https://openrouter.ai/docs/guides/features/zero-completion-insurance.md)
describes protections based on output and finish reason. The Decisions schema
does not expose finish reason. No Decisions-specific idempotency or lost-response
billing guarantee was found. The [general error guide](https://openrouter.ai/docs/api_reference/errors-and-debugging.md)
also contains qualifications about prompt processing without generated output.
Shearcast therefore does not turn an absent response into proven free work.

Before sending a request, Shearcast saves a request-start checkpoint. On its
completion it saves the observed usage. Concurrent callbacks are serialized;
an episode waits for its dispatched requests before finalizing its subtotal.
A usage-checkpoint write failure stops further episode/channel processing in
the invocation. Already-dispatched requests can still complete and incur usage;
the final CLI summary includes their observed usage even if saving it fails.

A hard stop can leave unresolved requests or an attempt with no recorded
completion. Saved observations remain available, but totals may be incomplete.
The start checkpoint is conservative: a stop immediately before dispatch can
leave an unresolved request that never reached OpenRouter. There is no automatic
billing reconciliation.

## Compact saved history

State version 5 stores the latest processing attempt and cumulative recorded
usage per episode, plus each channel's latest sync and the latest invocation.
Episode totals are labeled with their tracking start time. They include failed
attempts and survive pruning. They are not a complete pre-upgrade lifetime total.
Older interrupted attempts retain a count when a new attempt replaces their
detail. This is compact history, not a dated ledger of every request or run.

Usage checkpoints replace an attempt's absolute subtotal, updating episode and
sync totals atomically by the difference. Repeated checkpoints and publication
recovery do not add the same model work again. Manual processing does not change
saved sync totals. Completed publication retries, metadata refreshes and waiting
for captions contribute zero new model work in the current invocation.

New render records use version 2 and retain usage and rate provenance. Version 1
records remain readable. When an old render has no new accounting history,
`episode list` displays its saved input tokens and saved calculated amount as
`legacy/unverified`, without repricing it. Output tokens and provider cost were
not saved in those records. An import without usage remains unknown. Historical
artifact observations are not added to current run spending or backfilled into
the new cumulative counters. Replacing an old artifact starts new tracked
history; the old artifact is not an archive of earlier attempts.

An updated build reads older state without rewriting it. The next successful
mutation writes version 6. Use the current build for all commands sharing state;
older builds reject this version. Private accounting remains in state/cache,
not in published RSS or media storage.

## Commands and estimates

- `render`, `episode restore/reprocess` and `publish` print invocation model usage,
  including on processing failure.
- `sync` prints per-episode observations and a final invocation total, including
  failed episodes and partial channel failures.
- `episode list -channel SLUG` shows latest-attempt and cumulative recorded
  episode usage, or the available historical artifact observation.
- `status` shows saved totals for the latest invocation and channel attempts.
  It remains offline, read-only and available while a writer holds the lock.

`sync -dry-run` continues to use historical input tokens per source second from
the same channel, model and rules. It applies the currently recorded verified
rate to the existing broad planning range. It does not reprice saved spending.
New records with missing token usage or unresolved requests are excluded as
samples. Legacy successful records can still supply historical input counts;
their original retry coverage is unknown. Missing history, duration or verified
model pricing produces an insufficient-data result. A plan requiring no model
work has known zero model cost. Failed attempts and additional retries can make
actual usage exceed the planning range.
