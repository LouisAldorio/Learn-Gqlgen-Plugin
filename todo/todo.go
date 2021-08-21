package todo

import (
	"fmt"
	"go/types"
	"sort"

	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
	"github.com/99designs/gqlgen/plugin"
	"github.com/vektah/gqlparser/v2/ast"
)

type BuildMutateHook = func(b *ModelBuild) *ModelBuild

func defaultBuildMutateHook(b *ModelBuild) *ModelBuild {
	return b
}

type ModelBuild struct {
	PackageName string
	Interfaces  []*Interface
	Models      []*Object
	Enums       []*Enum
	Scalars     []string
}

type Interface struct {
	Description string
	Name        string
}

type Object struct {
	Description string
	Name        string
	Fields      []*Field
	Implements  []string
}

type Field struct {
	Description string
	Name        string
	Type        types.Type
	Tag         string
	Gorm        string
}

type Enum struct {
	Description string
	Name        string
	Values      []*EnumValue
}

type EnumValue struct {
	Description string
	Name        string
}

type Plugin struct {
	MutateHook BuildMutateHook
}

func New() plugin.Plugin {
	return &Plugin{
		MutateHook: defaultBuildMutateHook,
	}
}

var _ plugin.ConfigMutator = &Plugin{}

func (m *Plugin) Name() string {
	return "todo-modelgen"
}

func (m *Plugin) MutateConfig(cfg *config.Config) error {
	binder := cfg.NewBinder()

	b := &ModelBuild{
		PackageName: cfg.Model.Package,
	}

	for _, schemaType := range cfg.Schema.Types {

		if schemaType.BuiltIn {
			continue
		}
		
		switch schemaType.Kind {
		case ast.Interface, ast.Union:
			it := &Interface{
				Description: schemaType.Description,
				Name:        schemaType.Name,
			}

			b.Interfaces = append(b.Interfaces, it)
		case ast.Object, ast.InputObject:
			if schemaType == cfg.Schema.Query || schemaType == cfg.Schema.Mutation || schemaType == cfg.Schema.Subscription {
				continue
			}
			it := &Object{
				Description: schemaType.Description,
				Name:        schemaType.Name,
			}
			for _, implementor := range cfg.Schema.GetImplements(schemaType) {
				it.Implements = append(it.Implements, implementor.Name)
			}

			for _, field := range schemaType.Fields {
				var typ types.Type
				fieldDef := cfg.Schema.Types[field.Type.Name()]

				if cfg.Models.UserDefined(field.Type.Name()) {
					var err error
					typ, err = binder.FindTypeFromName(cfg.Models[field.Type.Name()].Model[0])
					if err != nil {
						return err
					}
				} else {
					switch fieldDef.Kind {
					case ast.Scalar:
						// no user defined model, referencing a default scalar
						typ = types.NewNamed(
							types.NewTypeName(0, cfg.Model.Pkg(), "string", nil),
							nil,
							nil,
						)

					case ast.Interface, ast.Union:
						// no user defined model, referencing a generated interface type
						typ = types.NewNamed(
							types.NewTypeName(0, cfg.Model.Pkg(), templates.ToGo(field.Type.Name()), nil),
							types.NewInterfaceType([]*types.Func{}, []types.Type{}),
							nil,
						)

					case ast.Enum:
						// no user defined model, must reference a generated enum
						typ = types.NewNamed(
							types.NewTypeName(0, cfg.Model.Pkg(), templates.ToGo(field.Type.Name()), nil),
							nil,
							nil,
						)

					case ast.Object, ast.InputObject:
						// no user defined model, must reference a generated struct
						typ = types.NewNamed(
							types.NewTypeName(0, cfg.Model.Pkg(), templates.ToGo(field.Type.Name()), nil),
							types.NewStruct(nil, nil),
							nil,
						)

					default:
						panic(fmt.Errorf("unknown ast type %s", fieldDef.Kind))
					}
				}

				name := field.Name
				if nameOveride := cfg.Models[schemaType.Name].Fields[field.Name].FieldName; nameOveride != "" {
					name = nameOveride
				}

				typ = binder.CopyModifiersFromAst(field.Type, typ)

				if isStruct(typ) && (fieldDef.Kind == ast.Object || fieldDef.Kind == ast.InputObject) {
					typ = types.NewPointer(typ)
				}

				gormType := ""
				directive := field.Directives.ForName("isDatabaseField")
				if directive != nil {
					arg := directive.Arguments.ForName("fieldName")
					if arg != nil {
						gormType = fmt.Sprintf(`gorm:"column:%s"`, arg.Value.Raw)
					}else {
						gormType = fmt.Sprintf(`gorm:"column:%s"`, field.Name)
					}					
				}

				it.Fields = append(it.Fields, &Field{
					Name:        name,
					Type:        typ,
					Description: field.Description,
					Tag:         `json:"` + field.Name + `"`,
					Gorm:        gormType,
				})
			}

			b.Models = append(b.Models, it)
		case ast.Enum:
			it := &Enum{
				Name:        schemaType.Name,
				Description: schemaType.Description,
			}

			for _, v := range schemaType.EnumValues {
				it.Values = append(it.Values, &EnumValue{
					Name:        v.Name,
					Description: v.Description,
				})
			}

			b.Enums = append(b.Enums, it)
		case ast.Scalar:
			b.Scalars = append(b.Scalars, schemaType.Name)
		}
	}
	sort.Slice(b.Enums, func(i, j int) bool { return b.Enums[i].Name < b.Enums[j].Name })
	sort.Slice(b.Models, func(i, j int) bool { return b.Models[i].Name < b.Models[j].Name })
	sort.Slice(b.Interfaces, func(i, j int) bool { return b.Interfaces[i].Name < b.Interfaces[j].Name })

	for _, it := range b.Enums {
		cfg.Models.Add(it.Name, cfg.Model.ImportPath()+"."+templates.ToGo(it.Name))
	}
	for _, it := range b.Models {
		cfg.Models.Add(it.Name, cfg.Model.ImportPath()+"."+templates.ToGo(it.Name))
	}
	for _, it := range b.Interfaces {
		cfg.Models.Add(it.Name, cfg.Model.ImportPath()+"."+templates.ToGo(it.Name))
	}
	for _, it := range b.Scalars {
		cfg.Models.Add(it, "github.com/99designs/gqlgen/graphql.String")
	}

	if len(b.Models) == 0 && len(b.Enums) == 0 && len(b.Interfaces) == 0 && len(b.Scalars) == 0 {
		return nil
	}

	return templates.Render(templates.Options{
		PackageName:     cfg.Model.Package,
		Filename:        cfg.Model.Filename,
		Data:            b,
		GeneratedHeader: true,
		Packages:        cfg.Packages,
		Template: `
			{{ reserveImport "context"  }}
			{{ reserveImport "fmt"  }}
			{{ reserveImport "io"  }}
			{{ reserveImport "strconv"  }}
			{{ reserveImport "time"  }}
			{{ reserveImport "sync"  }}
			{{ reserveImport "errors"  }}
			{{ reserveImport "bytes"  }}
			
			{{ reserveImport "github.com/vektah/gqlparser/v2" }}
			{{ reserveImport "github.com/vektah/gqlparser/v2/ast" }}
			{{ reserveImport "github.com/99designs/gqlgen/graphql" }}
			{{ reserveImport "github.com/99designs/gqlgen/graphql/introspection" }}
			
			{{- range $model := .Interfaces }}
				{{ with .Description }} {{.|prefixLines "// "}} {{ end }}
				type {{.Name|go }} interface {
					Is{{.Name|go }}()
				}
			{{- end }}
			
			{{ range $model := .Models }}
				{{with .Description }} {{.|prefixLines "// "}} {{end}}
				type {{ .Name|go }} struct {
					{{- range $field := .Fields }}
						{{- with .Description }}
							{{.|prefixLines "// "}}
						{{- end}}
						{{ $field.Name|go }} {{$field.Type | ref}}` + "`{{$field.Tag}} {{$field.Gorm}}`" + `
					{{- end }}
				}
			
				{{- range $iface := .Implements }}
					func ({{ $model.Name|go }}) Is{{ $iface|go }}() {}
				{{- end }}
			{{- end}}
			
			{{ range $enum := .Enums }}
				{{ with .Description }} {{.|prefixLines "// "}} {{end}}
				type {{.Name|go }} string
				const (
				{{- range $value := .Values}}
					{{- with .Description}}
						{{.|prefixLines "// "}}
					{{- end}}
					{{ $enum.Name|go }}{{ .Name|go }} {{$enum.Name|go }} = {{.Name|quote}}
				{{- end }}
				)
			
				var All{{.Name|go }} = []{{ .Name|go }}{
				{{- range $value := .Values}}
					{{$enum.Name|go }}{{ .Name|go }},
				{{- end }}
				}
			
				func (e {{.Name|go }}) IsValid() bool {
					switch e {
					case {{ range $index, $element := .Values}}{{if $index}},{{end}}{{ $enum.Name|go }}{{ $element.Name|go }}{{end}}:
						return true
					}
					return false
				}
			
				func (e {{.Name|go }}) String() string {
					return string(e)
				}
			
				func (e *{{.Name|go }}) UnmarshalGQL(v interface{}) error {
					str, ok := v.(string)
					if !ok {
						return fmt.Errorf("enums must be strings")
					}
			
					*e = {{ .Name|go }}(str)
					if !e.IsValid() {
						return fmt.Errorf("%s is not a valid {{ .Name }}", str)
					}
					return nil
				}
			
				func (e {{.Name|go }}) MarshalGQL(w io.Writer) {
					fmt.Fprint(w, strconv.Quote(e.String()))
				}
			
			{{- end }}
		`,
	})
}

func isStruct(t types.Type) bool {
	_, is := t.Underlying().(*types.Struct)
	return is
}

func (r *Plugin) InjectSourceEarly() *ast.Source {
	return &ast.Source{
		Name: "todo/directives.graphql",
		Input: `
			directive @goField(
				forceResolver: Boolean
				name: String
			  ) on INPUT_FIELD_DEFINITION | FIELD_DEFINITION

			  directive @isDatabaseField(fieldName: String) on OBJECT | FIELD_DEFINITION

			scalar Time
		`,
		BuiltIn: false,
	}
}
