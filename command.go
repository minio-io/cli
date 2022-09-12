package cli

import (
	"fmt"
	"io/ioutil"
	"sort"
	"strings"
)

// Command is a subcommand for a cli.App.
type Command struct {
	// The name of the command
	Name string
	// short name of the command. Typically one character (deprecated, use `Aliases`)
	ShortName string
	// A list of aliases for the command
	Aliases []string
	// A short description of the usage of this command
	Usage string
	// Custom text to show on USAGE section of help
	UsageText string
	// A longer explanation of how the command works
	Description string
	// A short description of the arguments of this command
	ArgsUsage string
	// The category the command is part of
	Category string
	// The function to call when checking for bash command completions
	BashComplete BashCompleteFunc
	// An action to execute before any sub-subcommands are run, but after the context is ready
	// If a non-nil error is returned, no sub-subcommands are run
	Before BeforeFunc
	// An action to execute after any subcommands are run, but after the subcommand has finished
	// It is run even if Action() panics
	After AfterFunc
	// The function to call when this command is invoked
	Action interface{}
	// TODO: replace `Action: interface{}` with `Action: ActionFunc` once some kind
	// of deprecation period has passed, maybe?

	// Execute this function if a usage error occurs.
	OnUsageError OnUsageErrorFunc
	// List of child commands
	Subcommands Commands
	// List of flags to parse
	Flags []Flag
	// Treat all flags as normal arguments if true
	SkipFlagParsing bool
	// Skip argument reordering which attempts to move flags before arguments,
	// but only works if all flags appear after all arguments. This behavior was
	// removed n version 2 since it only works under specific conditions so we
	// backport here by exposing it as an option for compatibility.
	SkipArgReorder bool
	// Boolean to hide built-in help flag
	HideHelp bool
	// Boolean to hide built-in help command
	HideHelpCommand bool
	// Boolean to hide this command from help or completion
	Hidden bool

	// Full name of command for help, defaults to full command name, including parent commands.
	HelpName        string
	commandNamePath []string

	// Default prompt, specific to OS
	Prompt string
	// Command to set the environment variable, specific to OS
	EnvVarSetCommand string
	// Assignment operator to set the environment variable, specific to OS
	AssignmentOperator string
	// Disable history for security reasons
	DisableHistory string
	// Enable history
	EnableHistory string

	// CustomHelpTemplate the text template for the command help topic.
	// cli.go uses text/template to render templates. You can
	// render custom help text by setting this variable.
	CustomHelpTemplate string
}

type CommandsByName []Command

func (c CommandsByName) Len() int {
	return len(c)
}

func (c CommandsByName) Less(i, j int) bool {
	return c[i].Name < c[j].Name
}

func (c CommandsByName) Swap(i, j int) {
	c[i], c[j] = c[j], c[i]
}

// FullName returns the full name of the command.
// For subcommands this ensures that parent commands are part of the command path
func (c Command) FullName() string {
	if c.commandNamePath == nil {
		return c.Name
	}
	return strings.Join(c.commandNamePath, " ")
}

// Commands is a slice of Command
type Commands []Command

type isBoolFlag interface {
	IsBoolFlag() bool
}

// Run invokes the command given the context, parses ctx.Args() to generate command-specific flags
func (c Command) Run(ctx *Context) (err error) {
	if len(c.Subcommands) > 0 {
		return c.startApp(ctx)
	}

	if !c.HideHelp && (HelpFlag != BoolFlag{}) {
		// append help to flags
		c.Flags = append(
			c.Flags,
			HelpFlag,
		)
	}

	set, err := flagSet(c.Name, c.Flags)
	if err != nil {
		return err
	}
	set.SetOutput(ioutil.Discard)

	if c.SkipFlagParsing {
		err = set.Parse(append([]string{"--"}, ctx.Args().Tail()...))
	} else if !c.SkipArgReorder {
		var (
			regularArgs, flagArgs []string
			terminatorIndex       = -1
			isFlagArg             bool
		)

		for index, arg := range ctx.Args().Tail() {
			doubleHyphen := false

			switch {
			case terminatorIndex > -1:
				regularArgs = append(regularArgs, arg)
			case isFlagArg:
				flagArgs = append(flagArgs, arg)
				isFlagArg = false
			case arg == "--":
				terminatorIndex = index
				regularArgs = append(regularArgs, arg)
			case arg == "-":
				regularArgs = append(regularArgs, arg)
			case strings.HasPrefix(arg, "--"):
				doubleHyphen = true
				fallthrough
			case strings.HasPrefix(arg, "-"):
				flagArgs = append(flagArgs, arg)
				if eq := strings.Index(arg, "="); eq > -1 {
					break
				}

				hyphens := "-"
				if doubleHyphen {
					hyphens += "-"
				}
				flagName := strings.TrimPrefix(arg, hyphens)
				f := set.Lookup(flagName)
				if f != nil {
					fv, ok := f.Value.(isBoolFlag)
					isFlagArg = !ok || !fv.IsBoolFlag()
				}
			default:
				regularArgs = append(regularArgs, arg)
			}
		}

		err = set.Parse(append(flagArgs, regularArgs...))
	} else {
		err = set.Parse(ctx.Args().Tail())
	}

	nerr := normalizeFlags(c.Flags, set)
	if nerr != nil {
		fmt.Fprintln(ctx.App.Writer, nerr)
		fmt.Fprintln(ctx.App.Writer)
		return nerr
	}

	context := NewContext(ctx.App, set, ctx)
	context.Command = c
	if checkCommandCompletions(context, c.Name) {
		return nil
	}

	if err != nil {
		if c.OnUsageError != nil {
			err := c.OnUsageError(context, err, false)
			HandleExitCoder(err)
			return err
		}
		fmt.Fprintln(context.App.Writer, "Incorrect Usage:", err.Error())
		fmt.Fprintln(context.App.Writer)
		return err
	}

	if checkCommandHelp(context, c.Name) {
		return nil
	}

	if c.After != nil {
		defer func() {
			afterErr := c.After(context)
			if afterErr != nil {
				HandleExitCoder(err)
				if err != nil {
					err = NewMultiError(err, afterErr)
				} else {
					err = afterErr
				}
			}
		}()
	}

	if c.Before != nil {
		err = c.Before(context)
		if err != nil {
			fmt.Fprintln(context.App.Writer, err)
			fmt.Fprintln(context.App.Writer)
			HandleExitCoder(err)
			return err
		}
	}

	if c.Action == nil {
		c.Action = helpSubcommand.Action
	}

	err = HandleAction(c.Action, context)

	if err != nil {
		HandleExitCoder(err)
	}
	return err
}

// Names returns the names including short names and aliases.
func (c Command) Names() []string {
	names := []string{c.Name}

	if c.ShortName != "" {
		names = append(names, c.ShortName)
	}

	return append(names, c.Aliases...)
}

// HasName returns true if Command.Name or Command.ShortName matches given name
func (c Command) HasName(name string) bool {
	for _, n := range c.Names() {
		if n == name {
			return true
		}
	}
	return false
}

func (c Command) startApp(ctx *Context) error {
	app := NewApp()
	app.Metadata = ctx.App.Metadata
	// set the name and usage
	app.Name = fmt.Sprintf("%s %s", ctx.App.Name, c.Name)
	if c.HelpName == "" {
		app.HelpName = c.HelpName
	} else {
		app.HelpName = app.Name
	}

	app.Usage = c.Usage
	app.Description = c.Description
	app.ArgsUsage = c.ArgsUsage

	// set CommandNotFound
	app.CommandNotFound = ctx.App.CommandNotFound
	app.CustomAppHelpTemplate = c.CustomHelpTemplate

	// set the flags and commands
	app.Commands = c.Subcommands
	app.Flags = c.Flags
	app.HideHelp = c.HideHelp
	app.HideHelpCommand = c.HideHelpCommand

	app.Version = ctx.App.Version
	app.HideVersion = ctx.App.HideVersion
	app.Compiled = ctx.App.Compiled
	app.Author = ctx.App.Author
	app.Email = ctx.App.Email
	app.Writer = ctx.App.Writer
	app.HelpWriter = ctx.App.HelpWriter
	app.ErrWriter = ctx.App.ErrWriter

	app.categories = CommandCategories{}
	for _, command := range c.Subcommands {
		app.categories = app.categories.AddCommand(command.Category, command)
	}

	sort.Sort(app.categories)

	// bash completion
	app.EnableBashCompletion = ctx.App.EnableBashCompletion
	if c.BashComplete != nil {
		app.BashComplete = c.BashComplete
	}

	// set the actions
	app.Before = c.Before
	app.After = c.After
	if c.Action != nil {
		app.Action = c.Action
	} else {
		app.Action = helpSubcommand.Action
	}

	for index, cc := range app.Commands {
		app.Commands[index].commandNamePath = []string{c.Name, cc.Name}
	}

	return app.RunAsSubcommand(ctx)
}

// VisibleFlags returns a slice of the Flags with Hidden=false
func (c Command) VisibleFlags() []Flag {
	flags := c.Flags
	if !c.HideHelp && (HelpFlag != BoolFlag{}) {
		// append help to flags
		flags = append(
			flags,
			HelpFlag,
		)
	}
	return visibleFlags(flags)
}
