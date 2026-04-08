# SkyShop

E-commerce application built with Sky.Live. Features Stripe Checkout payments, Firebase authentication, Firestore database, internationalisation (English/Chinese), and an admin panel.

## Build & Run

```bash
sky install
cp .env.example .env
# Edit .env with your Stripe and Firebase credentials
sky build src/Main.sky
./sky-out/app
```

Open `http://localhost:8000` in your browser.

## Prerequisites

- A Firebase project with Firestore and Authentication enabled
- A Stripe account (test mode) -- set `STRIPE_API_KEY` in `.env`
- Firebase Admin SDK credentials saved as `firebaseadminsdk.json`

See `.env.example` for all required environment variables.
