package service

import (
 "errors"
 "path/filepath"
 "testing"
 "x-ui/database"
 "x-ui/database/model"
)

func TestAgentReloadFailureRemainsRetryable(t *testing.T) {
 if err:=database.InitDB(filepath.Join(t.TempDir(),"reload.db")); err!=nil { t.Fatal(err) }
 old:=restartManagedXray
 t.Cleanup(func(){restartManagedXray=old})
 calls:=0
 restartManagedXray=func()error{calls++;if calls==1{return errors.New("restart failed")};return nil}
 if err:=ApplyManagedXray();err==nil{t.Fatal("expected restart error")}
 var pending int64
 if err:=database.GetDB().Model(&model.Setting{}).Where("key = ?",agentReloadKey).Count(&pending).Error;err!=nil||pending!=1{t.Fatalf("reload intent lost: %d %v",pending,err)}
 if err:=EnsureManagedXray();err!=nil{t.Fatal(err)}
 if err:=database.GetDB().Model(&model.Setting{}).Where("key = ?",agentReloadKey).Count(&pending).Error;err!=nil||pending!=0{t.Fatalf("reload intent not cleared: %d %v",pending,err)}
}
