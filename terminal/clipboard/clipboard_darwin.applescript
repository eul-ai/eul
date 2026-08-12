on run argv
	try
		set imageData to the clipboard as «class PNGf»
	on error
		return "no image"
	end try

	set outputPath to item 1 of argv
	set outputFile to open for access POSIX file outputPath with write permission
	try
		set eof outputFile to 0
		write imageData to outputFile
		close access outputFile
	on error message
		try
			close access outputFile
		end try
		error message
	end try

	return "ok"
end run
