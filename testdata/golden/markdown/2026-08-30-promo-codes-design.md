# Promotional codes — design

Date: 2026-08-30
Status: approved (design), not yet implemented

## Problem

There is no way to sell the manual at anything other than `Purchase::PRICE_CENTS`.
Running a campaign today means either changing the constant for everyone or
recording sales by hand through `/antesis-admin/enrollments`.

We want a promotional code: a buyer types a string at `/pagar`, the total drops
by a fixed amount, and the sale is recorded with the code that produced it.

## Scope

In scope:

- One discount kind: a **fixed amount off**, in centavos. No percentages.
- An **expiry datetime** and an **active/inactive** flag.
- **Unlimited uses**, globally and per buyer. A second purchase by the same
  email with the same code is a legitimate second sale, exactly as a repeat
  Stripe purchase already is.
- All three payment paths: Stripe Payment Element, PayPal, and the SPEI bank
  transfer.
- An admin screen to create codes, edit live ones, and see who redeemed each.

Out of scope (YAGNI):

- Percentage discounts, per-code usage caps, per-email limits, product scoping,
  stacking two codes, free (100%-off) purchases, and attaching a code to an
  admin enrollment at `/antesis-admin/enrollments`.

## Decisions

| Question | Decision |
|---|---|
| Discount kind | Fixed centavos off |
| Payment methods | Stripe, PayPal, and bank transfer |
| Expired / inactive code at payment time | **Never honored.** Every money endpoint re-validates on every call. |
| Where the redemption is recorded | Columns on `purchases`, no join table |
| Floor | Charge clamps at `MINIMUM_CHARGE_CENTS` (1000 = $10 MXN) |
| Expiry | Required datetime |
| Matching | Case-insensitive, admin-authored string |
| Uses | Unlimited, no per-email check |
| Admin UI | Own Inertia page under `/antesis-admin`, linked from `/a` |
| Editing a code | All fields while it has zero redemptions; once redeemed, only `active` |

## Approach

**A server-side price resolver, plus attribution carried in the processor
object.**

The client may send a *code string*. It may never send an amount. Every endpoint
that spends money resolves the price itself through one object, re-validating
`active` and `expires_at` on each call — so a code that expires between the
buyer applying it and paying is simply not applied, and a hand-crafted POST
quoting a dead code gets full price.

Attribution has to survive into the **webhook**, which carries no request
params and can be the path that records the purchase. So the resolved code id
rides in the processor object the same way terms acceptance and the product tag
already do: Stripe `metadata.promo_code_id`, PayPal `custom_id`.

Two alternatives were rejected:

- **Session-carried code.** Storing the validated code in the Rails session
  adds a second source of truth the webhook cannot read, and since expired codes
  are never honored, per-call re-validation is required regardless.
- **Pending-redemption row keyed by the idempotency key.** PayPal's capture id
  differs from the order id, so the key does not survive to the webhook.
  `custom_id` does.

## Data model

### `promo_codes` (new)

| Column | Type | Notes |
|---|---|---|
| `code` | string, not null | As typed by the admin. Unique index on `LOWER(code)` — the `users.nickname` pattern. |
| `discount_cents` | bigint, not null | Validated `> 0`. |
| `expires_at` | datetime, not null | |
| `active` | boolean, not null, default `true` | |

Model:

```ruby
class PromoCode < ApplicationRecord
  MINIMUM_CHARGE_CENTS = 1_000 # $10 MXN — Stripe's MXN floor.

  has_many :purchases

  validates :code, presence: true, uniqueness: { case_sensitive: false }
  validates :discount_cents, numericality: { only_integer: true, greater_than: 0 }
  validates :expires_at, presence: true

  # Lookup is case-insensitive, hitting the LOWER(code) index.
  def self.find_by_code(raw)
    where("LOWER(code) = ?", raw.to_s.strip.downcase).first
  end

  def redeemable? = active? && expires_at.future?
  def redeemed? = purchases.exists?
end
```

As with `users.nickname`, the **index is the authority** and the validation only
proposes: any insert path must be ready for both `RecordNotUnique` and a
`RecordInvalid` with `code: :taken`, which mean the same thing.

### `purchases` (altered)

| Column | Type | Notes |
|---|---|---|
| `promo_code_id` | bigint, nullable, FK, indexed | Nil for a full-price sale. |
| `discount_cents` | bigint, not null, default `0` | What this sale was discounted by. |

`amount_cents` keeps its existing meaning — **what was actually charged** — and
stays sourced from the Stripe/PayPal object, never from a constant. The two new
columns only annotate it. Consequently, editing a `promo_codes` row never
rewrites what a past sale recorded, which is the same non-overwrite guarantee
`amount_cents` was introduced for (see
`db/migrate/20260825120000_add_amount_to_purchases.rb`).

Migration backfills nothing: `promo_code_id` is nil and `discount_cents` is 0
for every existing row, which is already true of them.

### `bank_transfer_payment_intents` (altered)

| Column | Type | Notes |
|---|---|---|
| `amount_cents` | bigint, not null | Backfilled with the literal `69000`, not `PRICE_CENTS`. |
| `promo_code_id` | bigint, nullable, FK, indexed | |

The reference is a ledger of what we *told a buyer to deposit*, so the amount we
quoted has to be on the row: otherwise a deposit for an unexpected sum cannot be
explained from the database. Same backfill-with-a-literal rule as the
`purchases.amount_cents` migration — the constant may change later; history may
not.

## Pricing

One object is the authority for what a checkout costs:

```ruby
# app/concepts/checkout/price.rb (or app/services/checkout_price.rb)
Checkout::Price.resolve(code_string) # => Checkout::Price
#   #amount_cents    what to charge, clamped at PromoCode::MINIMUM_CHARGE_CENTS
#   #discount_cents  PRICE_CENTS - amount_cents (the *effective* discount)
#   #promo_code      the PromoCode, or nil
#   #label           "$490 MXN"
```

Rules:

- A blank, unknown, inactive or expired code resolves to full price with
  `promo_code: nil`. It is not an error at the payment endpoints — the buyer
  just pays full price, and the apply endpoint (below) is where a bad code
  produces a visible message.
- `amount_cents = [PRICE_CENTS - discount_cents, MINIMUM_CHARGE_CENTS].max`.
  A code worth more than the manual charges $10 rather than going negative.
- `discount_cents` is recomputed from the clamped amount, so a clamped sale
  records the discount it actually received, not the code's nominal value.

`Purchase.price_label` gains an optional argument
(`def self.price_label(cents = PRICE_CENTS)`) and keeps deriving the string from
cents, so the shown and charged amounts still cannot drift.

## Checkout endpoints

### `POST /pagar/promocion` — apply

`CheckoutsController#apply_promo_code`. CSRF-protected like its neighbours, and
rate-limited under its own `name:` (`"promo-code"`, 10/min) — a shared `name:`
would let one endpoint eat another's budget, which is why the existing limits
are already named.

Request: `{ code }`. No email, no terms — this reveals nothing and spends
nothing.

Response, on a redeemable code:

```json
{ "code": "LANZAMIENTO", "discountLabel": "-$200 MXN",
  "priceLabel": "$490 MXN", "amountCents": 49000 }
```

Otherwise `422` with `{ "error": "invalid" }`; the page renders one Spanish
message ("Ese código no es válido o ya expiró") for unknown, inactive and
expired alike — distinguishing them would tell a stranger which codes exist.

`amountCents` is returned solely so the client can update the Stripe Elements
options (see below). It is display data; the charge is still computed
server-side on every payment call.

### The three payment endpoints

Each takes an optional `promo_code` param, resolves it, and uses the resolved
amount:

- **`CheckoutsController#create_intent`** — creates the PaymentIntent at
  `price.amount_cents` and adds `promo_code_id` to the metadata beside the
  existing `product` and `terms_accepted_at`.
- **`Paypal::CheckoutsController#create_order`** — creates the order at
  `price.amount_cents` and passes the code id into `custom_id`.
- **`CheckoutsController#create_bank_transfer`** — passes
  `amount_cents:`/`promo_code:` into `BankTransferPaymentIntent.for`, which now
  **updates** those two columns on an existing row when they differ. The
  reference itself never changes; one email keeps one reference forever, which
  is the invariant that makes a deposit reconcilable.
  `#email_bank_transfer` and `BankTransferMailer#instructions` read
  `intent.amount_cents` instead of `Purchase.price_label`, so the mail always
  quotes what the modal quoted.

### `custom_id` format

Today: `"#{MANUAL_PRODUCT}|#{email}"`, parsed with `split("|", 2)`.
New: `"#{MANUAL_PRODUCT}|#{email}|#{promo_code_id}"`, third part optional.

`Purchase.paypal_custom_parts` changes to `split("|", 3)` and must keep
accepting a two-part value — orders created before this ships are still in
flight when it deploys. PayPal caps `custom_id` at 127 characters; product tag
plus a long email plus an integer id stays well inside it, but the builder
should drop the promo part rather than truncate if the whole ever exceeds it.

## Recording the purchase

`Purchase::PaymentSource` gains two members, `promo_code_id` and
`discount_cents`, filled by each constructor:

- `.from_payment_intent` — reads `intent.metadata["promo_code_id"]`, and derives
  `discount_cents` as `PRICE_CENTS - intent.amount` floored at 0. Deriving it
  from the intent's own amount (rather than re-reading the code) keeps the rule
  that a sale records what it actually charged.
- `.from_paypal_capture` — the same, from the third `custom_id` part and the
  capture's own amount.
- `.cash` — always `nil` / `0`. Attaching a code to an admin enrollment is out
  of scope; the admin types the amount they were actually paid.

`Purchase::Operation::Record#record_purchase` writes both new fields **only
inside the `create_or_find_by!` block**, exactly like `amount_cents` — a
re-delivered webhook or a second browser return must find the existing row and
leave its recorded discount untouched.

An unknown or since-deleted `promo_code_id` in metadata must not fail the
purchase: the write is `promo_code_id: id if PromoCode.exists?(id)`, or the FK
is left nil. A paid sale never fails over an annotation.

## Checkout page (`Checkout.jsx`)

Below the PayPal button: a text link **"agregar código promocional"** that
reveals a text input and an **"aplicar"** button (a `button`, not a nested
form — the page already has one form and `handleSubmit` pays). On success:

- The summary aside shows the original price struck through above the new
  total, and the pay button reads `Pagar $490 MXN`.
- A small "código LANZAMIENTO aplicado" line with a way to remove it.

Three implementation hazards, all of them the kind that pass a stubbed test and
fail in a real browser:

1. **`<Elements>` mounts in deferred-intent mode with a fixed `amount`.**
   Applying a code must change that option, or Stripe raises at confirm time on
   the amount mismatch. This means the applied-promo state must live in the
   top-level `Checkout` component (which renders `<Elements>`), not inside
   `CheckoutForm` — react-stripe-js forwards an `options.amount` change to
   `elements.update()`, so passing the new amount down is enough.
2. **The PayPal button's callbacks are zoid-hosted and capture stale
   closures.** `Checkout.jsx` already routes `email` and "can pay" through
   `emailRef`/`canPayRef` for this reason; the applied code needs a
   `promoCodeRef` on the same pattern, or `createOrder` will post the code as it
   stood when the button rendered.
3. **`parsePrice`** parses the display label only to feed Elements'
   payment-method picker. With a discount the top-level component now has
   `amountCents` directly from the apply response; prefer it when present and
   keep `parsePrice(priceLabel)` as the no-code path.

Everything else in the file's existing conventions holds: default-theme tokens
only, no browser globals outside effects/handlers (this page renders through
SSR), straight corners, hairlines.

The bank transfer modal needs no structural change — it already renders
`details.amountLabel`, which the server now computes from the intent's stored
amount.

## Admin

Route, inside the existing admin `authenticate` block and **before** the
RailsAdmin mount (which owns the whole `/antesis-admin` prefix):

```
get   "/antesis-admin/promo-codes",     to: "admin/promo_codes#index"
post  "/antesis-admin/promo-codes",     to: "admin/promo_codes#create"
patch "/antesis-admin/promo-codes/:id", to: "admin/promo_codes#update"
```

`Admin::PromoCodesController` renders one Inertia page, `Admin/PromoCodes`,
following `Admin::EnrollmentsController`'s shape: a private `render_page` that
re-renders the same component with an `error:` or a success marker, rather than
redirect-and-flash.

Props: each code with `code`, `discountLabel`, `expiresAt`, `active`,
`redemptionCount`, `editable` (= `redemptionCount.zero?`), and its redemptions
(buyer email, nickname, date, `discountLabel`, `amountLabel`) — one query with
`includes(:purchases)`, not one per row.

The page shows a create form, then a list of codes with state, expiry and
redemption count, each expandable to its redemptions.

**Editing rule.** While a code has zero redemptions, every field is editable.
Once it has been redeemed, only `active` may change — deactivating a live
campaign has to stay possible, but silently renaming or re-pricing a code that
buyers are holding must not. The controller enforces this server-side (it
filters the permitted params by `redeemed?`); the page merely reflects it, since
a disabled input is not a security boundary.

A link is added to `Admin::HubController`'s `links` array: "Códigos
promocionales" / "Crea y edita promociones, y mira quién las usó".

## Testing

RSpec (`spec/`):

- **`PromoCode`** — case-insensitive lookup and uniqueness; `redeemable?` across
  active/inactive × future/past expiry.
- **`Checkout::Price`** — full price for blank/unknown/inactive/expired;
  discounted price for a live code; the clamp, including that `discount_cents`
  reports the clamped (effective) discount, not the nominal one.
- **`POST /pagar/promocion`** — success shape; one identical 422 for unknown,
  inactive and expired; rate limit.
- **`create_intent`** — the intent is created at the discounted amount and
  carries `promo_code_id` in metadata; an expired code produces a full-price
  intent with no metadata code.
- **PayPal** — `create_order` uses the discounted amount; `custom_id` round-trips
  three parts, and a legacy two-part value still parses.
- **Stripe webhook** — a purchase recorded from a webhook alone (no browser
  return) lands with the right `promo_code_id` and `discount_cents`.
- **`Purchase::Operation::Record`** — a second arrival for the same idempotency
  key leaves `discount_cents` and `promo_code_id` untouched; an unknown
  `promo_code_id` records the purchase with a nil code rather than failing.
- **Bank transfer** — `.for` updates the amount on an existing reference without
  changing the reference number; the mailer quotes the stored amount.
- **Admin** — non-admin is refused; create/edit; a redeemed code rejects every
  field but `active` even when the params are posted directly.

No new Vitest coverage: the promo logic added to `Checkout.jsx` is React state
and fetch, not the pure-function kind that `lib/highlights/` extracts. The
Elements `amount` update and the PayPal ref are verified by driving the real
page in a browser, which is the only thing that catches this class of bug (the
`elements.submit()` ordering has the same property).

## Risks

- **The Elements amount mismatch** (hazard 1) is silent under every stub and
  loud only against a live Payment Element. Verify in a browser before shipping.
- **In-flight PayPal orders** created before deploy carry a two-part
  `custom_id`. The parser must stay tolerant.
- **A code edited mid-campaign** changes future sales only. That is intended and
  is what the `redeemed?` lock plus per-purchase `discount_cents` guarantee.
