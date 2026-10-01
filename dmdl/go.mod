module github.com/cloud-barista/cm-centipede/dmdl

// Kept at the lowest version the models compile under so that every consumer
// can depend on this module: a dependency whose go directive is higher than the
// main module's fails the build.
go 1.26.2
