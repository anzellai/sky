import os, shutil, sys
# Scale Msg arms + Model fields in one update (real web:app shape):
# per k: Model fields fK : String, nK : List Note; Msgs EditK String (pure),
# LoadK (server read), SaveK (server write reading fK). Input per k in view.
src, dst, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
if os.path.exists(dst):
    shutil.rmtree(dst)
shutil.copytree(src, dst, ignore=shutil.ignore_patterns('sky-out*', '.skyapp', '.split', 'dist', '*.db'))
p = os.path.join(dst, "src", "Main.sky")
m = open(p).read()
fields = "".join(f"\n    , f{k} : String\n    , n{k} : List Note" for k in range(1, n + 1))
m = m.replace("    , query : String\n    }", "    , query : String" + fields + "\n    }", 1)
inits = "".join(f"\n      , f{k} = \"\"\n      , n{k} = []" for k in range(1, n + 1))
m = m.replace('      , query = ""\n      }', '      , query = ""' + inits + "\n      }", 1)
variants = "".join(f"\n    | Edit{k} String\n    | Load{k}\n    | Save{k}" for k in range(1, n + 1))
m = m.replace("    | Delete Int\n", "    | Delete Int" + variants + "\n", 1)
br = "".join(f"""        Edit{k} s ->
            ( {{ model | f{k} = s }}, Cmd.none )

        Load{k} ->
            ( {{ model | n{k} = Persist.loadNotes () }}, Cmd.none )

        Save{k} ->
            let
                newId =
                    Persist.createNote (stamp{k} model) "{k}"
            in
            ( {{ model | n{k} = Persist.loadNotes (), selected = newId, f{k} = "" }}, Cmd.none )

""" for k in range(1, n + 1))
m = m.replace("update msg model =\n    case msg of\n", "update msg model =\n    case msg of\n" + br, 1)
views = "\n            , ".join(
    f'Ui.row [] [ Input.text [] {{ onChange = Edit{k}, text = model.f{k}, placeholder = Nothing, label = Input.labelHidden "f{k}" }}, Ui.el [ Ev.onClick (Save{k}) ] (Ui.text "save"), Ui.el [ Ev.onClick Load{k} ] (Ui.text (String.fromInt (List.length model.n{k}))) ]'
    for k in range(1, n + 1))
m = m.replace("            [ header model\n", f"            [ header model\n            , Ui.column []\n            [ {views}\n            ]\n", 1)
open(p, "w").write(m)
print(dst, n, sum(1 for _ in open(p)))
# whole-model helpers: each takes the Model and keeps it opaque via a list
helpers = "".join(f"\n\nstamp{k} : Model -> String\nstamp{k} m =\n    List.length [ m ] |> String.fromInt\n" for k in range(1, n + 1))
open(p, "a").write(helpers)
