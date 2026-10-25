# stripe

Stripe API. Setup: restricted or secret key `sk_...` as `STRIPE_SECRET_KEY`
allowed for `api.stripe.com`; grant `net.allow_hosts: ["api.stripe.com"]`.
Amounts are returned as decimal strings (minor units converted per currency).
