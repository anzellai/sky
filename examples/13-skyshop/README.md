# SkyShop -- E-commerce Example

A full-featured e-commerce application built with Sky.Live, demonstrating Firebase Auth, Firestore, Stripe payments, admin panel, i18n, and image uploads.

## Features

- Product catalogue with categories, search, and filtering
- Shopping cart with quantity management
- Stripe Checkout integration (hosted payment page)
- Order management with status workflow (pending -> ordered -> shipped -> delivered)
- Admin panel for products, images, and orders
- Firebase Authentication (Google/Facebook OAuth)
- Firestore database
- Bilingual support (English/Chinese)
- Product image uploads (base64 stored in Firestore)
- Real-time updates via SSE subscriptions
- Tailwind CSS styling via sky-tailwind

## Prerequisites

- [Sky](https://github.com/anzellai/sky) compiler installed
- [Go](https://go.dev/) 1.21+
- A Google Cloud / Firebase project
- A Stripe account (test mode is fine)

## Setup

### 1. Firebase Project

1. Go to [Firebase Console](https://console.firebase.google.com/) and create a new project (or use an existing one)
2. Note your **Project ID** (shown in Project Settings > General)

### 2. Firestore Database

1. In Firebase Console, go to **Build > Firestore Database**
2. Click **Create database**
3. Choose a location (e.g. `europe-west2` for UK, `us-central1` for US)
4. Start in **test mode** for development (you can add security rules later)

No manual collection/document setup is needed -- SkyShop creates collections automatically on first use.

### 3. Firebase Authentication

1. In Firebase Console, go to **Build > Authentication**
2. Click **Get started**
3. Enable the sign-in providers you want:

**Google Sign-In:**
1. Click **Google** in the sign-in providers list
2. Toggle **Enable**
3. Set your **Project support email**
4. Click **Save**
5. Note the **Web client ID** (this is your `GOOGLE_CLIENT_ID`)

**Facebook Sign-In** (optional):
1. Create an app at [Facebook Developers](https://developers.facebook.com/)
2. Add Facebook Login product
3. Copy the App ID and App Secret into Firebase Console
4. Add the OAuth redirect URI from Firebase to your Facebook app's Valid OAuth Redirect URIs

### 4. Google Cloud Console -- OAuth Consent Screen

This step is required for Google Sign-In to work:

1. Go to [Google Cloud Console](https://console.cloud.google.com/)
2. Select your Firebase project
3. Navigate to **APIs & Services > OAuth consent screen**
4. Choose **External** user type (unless you have a Google Workspace org)
5. Fill in:
   - **App name**: Your app name (e.g. "SkyShop")
   - **User support email**: Your email
   - **Developer contact email**: Your email
6. Click **Save and Continue** through the remaining steps
7. Under **Credentials** (or back in Firebase Auth settings), ensure your **Authorized redirect URIs** include:
   - `https://your-project.firebaseapp.com/__/auth/handler`
   - `http://localhost:4000` (for local development)

### 5. Firebase Admin SDK Credentials

1. In Firebase Console, go to **Project Settings > Service Accounts**
2. Click **Generate new private key**
3. Save the downloaded JSON file as `firebaseadminsdk.json` in the `examples/13-skyshop/` directory
4. **Never commit this file to git** -- it contains your project's private key

### 6. Firebase API Key

1. In Firebase Console, go to **Project Settings > General**
2. Scroll down to **Your apps** section
3. If no web app exists, click **Add app** > **Web** (`</>` icon)
4. Register the app (name doesn't matter)
5. Copy the `apiKey` value from the Firebase config snippet -- this is your `FIREBASE_API_KEY`
6. The `authDomain` value is your `AUTH_DOMAIN` (e.g. `your-project.firebaseapp.com`)

### 7. Stripe

1. Sign up at [Stripe Dashboard](https://dashboard.stripe.com/)
2. Make sure you're in **Test mode** (toggle in top right)
3. Go to **Developers > API keys**
4. Copy the **Secret key** (starts with `sk_test_`) -- this is your `STRIPE_API_KEY`

### 8. Environment Variables

Copy the example env file and fill in your values:

```bash
cd examples/13-skyshop
cp .env.example .env
```

Edit `.env`:

```bash
# Required
GOOGLE_CLOUD_PROJECT=your-firebase-project-id
GOOGLE_APPLICATION_CREDENTIALS=firebaseadminsdk.json
FIREBASE_API_KEY=AIzaSy...your-api-key
AUTH_DOMAIN=your-project.firebaseapp.com
GOOGLE_CLIENT_ID=123456789.apps.googleusercontent.com
STRIPE_API_KEY=sk_test_...your-stripe-secret-key
DOMAIN=http://localhost:4000

# Admin access (comma-separated emails that get admin privileges)
ADMIN_EMAILS=your-email@gmail.com

# Optional: customise port (default 4000)
SKY_LIVE_PORT=4000

# Optional: session persistence (default: memory, resets on restart)
# SKY_LIVE_SESSION_STORE=sqlite
# SKY_LIVE_SESSION_PATH=skyshop_sessions.db

# Optional: email notifications for orders
# SMTP_HOST=smtp.gmail.com
# SMTP_PORT=587
# SMTP_USER=your_email@gmail.com
# SMTP_PASS=your_app_password
# NOTIFY_TO=admin@example.com
```

## Build & Run

```bash
cd examples/13-skyshop
sky build
sky run
```

Or in one step:

```bash
sky run examples/13-skyshop/src/Main.sky
```

Open http://localhost:4000 in your browser.

## Usage

### First Run

1. Open the app and click **Sign In**
2. Sign in with Google (or Facebook if configured)
3. If your email is in `ADMIN_EMAILS`, you'll see the **Admin** link in the navigation

### Admin: Add Products

1. Click **Admin** in the top navigation
2. Go to **Products > New Product**
3. Fill in title, summary, category, price, stock
4. Toggle **Published** to make it visible on the storefront
5. Save, then upload product images on the edit page

### Customer: Purchase Flow

1. Browse products on the home page or products page
2. Click a product to see details, then **Add to Cart**
3. Go to **Cart** and adjust quantities
4. Click **Checkout** -- you'll be redirected to Stripe's hosted payment page
5. Use Stripe test card `4242 4242 4242 4242` (any future expiry, any CVC)
6. After payment, you're redirected back to the order confirmation page

### Order Management (Admin)

1. Go to **Admin > Orders**
2. Filter orders by status
3. Click an order to update its status (ordered -> shipped -> delivered)

## Project Structure

```
examples/13-skyshop/
  sky.toml                     -- Project manifest and dependencies
  .env.example                 -- Environment variable template
  firebaseadminsdk.json        -- Firebase credentials (not committed)
  static/
    uploads/                   -- Product images (created at runtime)
  src/
    Main.sky                   -- App entry: routing, init/update/view, event handlers
    State.sky                  -- Page/Msg/Model type definitions
    Lib/
      Auth.sky                 -- Firebase Auth: token verification, user management
      Db.sky                   -- Firestore: document CRUD, queries
      OAuth.sky                -- Client-side Firebase JS SDK for sign-in
      Stripe.sky               -- Stripe Checkout API integration
      Cart.sky                 -- Cart/order operations
      Products.sky             -- Product CRUD and queries
      Notify.sky               -- Email notifications (SMTP)
      Translation.sky          -- i18n (English/Chinese)
      Money.sky                -- Currency formatting
    Page/
      Home.sky                 -- Home page with featured products
      Product.sky              -- Product detail page
      CartPage.sky             -- Shopping cart page
      Orders.sky               -- Order history and detail pages
      AuthPage.sky             -- Sign-in page
      Admin.sky                -- Admin panel (products, orders, images)
      Static.sky               -- Static pages (privacy, terms)
    Ui/
      Layout.sky               -- Common layout, navigation, Firebase script embed
```

## Firestore Collections

SkyShop auto-creates these collections as data is written:

| Collection | Description |
|---|---|
| `users` | User accounts (id, email, name, address, is_admin) |
| `products` | Product catalogue (title, price, stock, category, published) |
| `product_images` | Base64-encoded product images |
| `carts` | Shopping carts / orders (state, totals, Stripe reference) |
| `cart_items` | Line items (product_id, quantity, denormalized price) |
| `notifications` | Order notification queue |

## Troubleshooting

### "Authentication service unavailable"

- Check that `firebaseadminsdk.json` exists and `GOOGLE_APPLICATION_CREDENTIALS` points to it
- Verify `GOOGLE_CLOUD_PROJECT` matches your Firebase project ID

### Google Sign-In popup closes without signing in

- Check `FIREBASE_API_KEY` and `AUTH_DOMAIN` are correct
- Verify the OAuth consent screen is configured in Google Cloud Console
- Make sure `http://localhost:4000` is in your authorized redirect URIs
- Check browser console for errors

### "Access denied" on admin pages

- Make sure your sign-in email is listed in `ADMIN_EMAILS` in `.env`
- Sign out and sign back in (admin status is checked on each login)

### Stripe checkout not working

- Verify `STRIPE_API_KEY` starts with `sk_test_` (not `pk_test_`)
- Check that `DOMAIN` is set correctly (e.g. `http://localhost:4000`)
- Look at server logs for Stripe API errors

### Products not appearing

- Make sure products are set to **Published** in the admin panel
- Check Firestore in Firebase Console to verify data was written
