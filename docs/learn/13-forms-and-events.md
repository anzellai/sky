# Forms & events

Buttons were the simplest event. Real apps have forms — and Sky has one strong
opinion about them, especially where passwords are involved.

## Events are messages

Every event maps to a typed `Msg`. The wire carries typed arguments per event:

| Event | Element | Argument |
|---|---|---|
| click | any | none |
| input / change | text, textarea, select | `String` value |
| input / change | number, range | `Float` value |
| input / change | checkbox | `Bool` checked |
| submit | form | the form data |
| keydown / keyup | any | `String` key |

So a text field that updates the model as you type is:

```elm
Ui.input
    [ Ui.onInput UpdateEmail ]
    { ... }
-- UpdateEmail : String -> Msg
```

## Forms submit a typed record

For a form, don't wire an `onInput` to every field. Put an `onSubmit` on the form
and let it deliver all the fields at once as a typed record:

```elm
type alias Creds =
    { email : String
    , password : String
    }

type Msg
    = SignIn Creds
    | UpdateEmail String


loginForm : Ui.Element Msg
loginForm =
    Ui.form [ Ui.onSubmit SignIn ]
        [ Ui.input [ Ui.onInput UpdateEmail ] { ... }   -- email is fine to track
        , passwordField                                 -- password is NOT
        , Ui.button [] { onPress = Just Submit, label = Ui.text "Sign in" }
        ]
```

On submit, the form's fields are decoded straight into the `Creds` record and
handed to `SignIn` — no per-field decoder boilerplate.

## The password rule

**Never put an `onInput` on a password field, and never store the password in your
Model.** Read it from the submitted form instead. Three reasons:

1. **Password managers** watch password inputs; a server re-render that sets
   `value=…` triggers a re-fill loop.
2. **The secret never touches your Model**, so it's never serialized into a
   session store.
3. **Submit reads the live value**, race-free.

So the password `<input>` has no `value` and no `onInput` — it just sits in the
form, and `onSubmit` collects it.

This is the one form pattern to internalize; the rest is ordinary events. More in
the [Sky.Live guide](../skylive/overview.md).

**[Next → Routing & navigation](14-routing.md)**
