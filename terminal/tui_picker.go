package terminal

const pickerMaxVisible = 5

type pickerList[T any] struct {
	items    []T
	selected int
}

func (list *pickerList[T]) move(direction int) {
	count := len(list.items)
	if count == 0 {
		return
	}
	list.selected = (list.selected + direction + count) % count
}

func (list pickerList[T]) current() (T, bool) {
	if list.selected < 0 || list.selected >= len(list.items) {
		var zero T
		return zero, false
	}
	return list.items[list.selected], true
}

func (list pickerList[T]) window(maximum int) ([]T, int) {
	start := 0
	if len(list.items) > maximum {
		start = max(0, min(list.selected-maximum/2, len(list.items)-maximum))
	}
	end := min(len(list.items), start+maximum)
	return list.items[start:end], list.selected - start
}

func renderPickerRows[T any](list pickerList[T], height int, row func(item T, index int) styledLine) []styledLine {
	visible, selected := list.window(height)
	lines := make([]styledLine, len(visible))
	for index, item := range visible {
		line := row(item, index)
		line.prefixText = "  "
		if index == selected {
			line.prefixText = "> "
			line.prefixForeground = &currentTheme.accent
			line.style = lineStyle{
				foreground:      currentTheme.foreground,
				background:      currentTheme.selectedBackground,
				paintBackground: true,
			}
		}
		lines[index] = line
	}
	return lines
}
